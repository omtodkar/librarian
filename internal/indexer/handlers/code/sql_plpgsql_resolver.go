package code

// sql_plpgsql_resolver.go — lib-o5dn.4: Dynamic SQL (EXECUTE) + trigger NEW/OLD resolution.
//
// Called by plpgsqlExtractRefs after the body walker collects pending_execute
// and trigger_special refs. Transforms those placeholder refs into fully
// qualified body_references with sym: targets (or drops them gracefully).

import (
	"log/slog"
	"strings"

	"librarian/internal/indexer"
	"librarian/internal/store"
)

// ResolveDynamicExecute resolves body_references refs that carry
// pending_execute=true (emitted by plpgsqlWalkDynExecute).
//
// Case A — string literal argument: parses the literal with the regular SQL
// parser, emits table refs with via_execute=true.
// Case B — variable / concatenation: cannot statically resolve; emits one
// marker ref with uses_dynamic_sql=true.
// Case C — literal whose content contains another EXECUTE: emits one marker
// ref with nested_execute=true; no recursion.
//
// declaredVars is currently unused (reserved for future assignment-tracking);
// pass nil.  All pending_execute flags are cleared before returning.
func ResolveDynamicExecute(refs []indexer.Reference, declaredVars map[string]bool) []indexer.Reference {
	var out []indexer.Reference

	for _, r := range refs {
		if r.Kind != store.EdgeKindBodyReferences {
			out = append(out, r)
			continue
		}
		pending, _ := r.Metadata["pending_execute"].(bool)
		if !pending {
			out = append(out, r)
			continue
		}

		// pending_execute ref — resolve and do NOT forward the original.
		funcPath := strings.TrimPrefix(r.Source, "sym:")
		defaultSchema := defaultSchemaFromFuncPath(funcPath)
		expr := r.Target

		if ok, literal := isStringLiteralExpr(expr); ok {
			// Check for nested EXECUTE inside the literal.
			if strings.Contains(strings.ToUpper(literal), "EXECUTE") {
				slog.Debug("plpgsql: nested EXECUTE in literal", "func", funcPath)
				out = append(out, markerRef(r.Source, funcPath, "nested_execute"))
				continue
			}
			// Parse the SQL literal and forward each table ref tagged via_execute.
			parsed := plpgsqlParseSQLRefs(literal, funcPath, defaultSchema)
			for i := range parsed {
				if parsed[i].Metadata == nil {
					parsed[i].Metadata = make(map[string]any)
				}
				parsed[i].Metadata["via_execute"] = true
			}
			out = append(out, parsed...)
		} else {
			// Variable or concatenation — cannot resolve statically.
			slog.Debug("plpgsql: dynamic SQL via EXECUTE with variable arg", "func", funcPath)
			out = append(out, markerRef(r.Source, funcPath, "uses_dynamic_sql"))
		}
	}
	return out
}

// ResolveTriggerNewOld resolves body_references refs that carry
// trigger_special=true (emitted by plpgsqlTriggerFieldRefs).
//
// For NEW.col: rewrites Target to sym:<schema>.<triggerTarget>.<col>,
// preserving the walker's op (read for datum-level refs; assignment-context
// detection is deferred to lib-uer8).
// For OLD.col: rewrites Target to sym:<schema>.<triggerTarget>.<col> with
// op=read.
// For NEW.* (wildcard, table-level read): emits a table-level ref.
//
// If triggerTarget is empty, trigger_special refs are dropped (cannot resolve
// without knowing which table the trigger is on).  All trigger_special flags
// are cleared before returning.
func ResolveTriggerNewOld(refs []indexer.Reference, triggerTarget, schema string) []indexer.Reference {
	var out []indexer.Reference

	for _, r := range refs {
		if r.Kind != store.EdgeKindBodyReferences {
			out = append(out, r)
			continue
		}
		special, _ := r.Metadata["trigger_special"].(bool)
		if !special {
			out = append(out, r)
			continue
		}

		// trigger_special ref — resolve and do NOT forward the original.
		if triggerTarget == "" {
			// Cannot resolve without the trigger target; drop silently.
			continue
		}

		target := r.Target
		// Preserve the walker's op; write-context detection (PLpgSQL_stmt_assign
		// scanning) is a future improvement tracked in lib-uer8.
		op, _ := r.Metadata["op"].(string)
		if op == "" {
			op = "read"
		}

		switch {
		// NOTE: current walker never emits NEW.* refs; this branch is a future
		// extension point for PLpgSQL SELECT NEW.* detection.
		case target == "NEW.*":
			tableTarget := "sym:" + schema + "." + triggerTarget
			out = append(out, indexer.Reference{
				Kind:     store.EdgeKindBodyReferences,
				Source:   r.Source,
				Target:   tableTarget,
				Metadata: map[string]any{"op": "read"},
			})

		case strings.HasPrefix(target, "NEW."):
			col := strings.TrimPrefix(target, "NEW.")
			symTarget := "sym:" + schema + "." + triggerTarget + "." + col
			out = append(out, indexer.Reference{
				Kind:     store.EdgeKindBodyReferences,
				Source:   r.Source,
				Target:   symTarget,
				Metadata: map[string]any{"op": op},
			})

		case strings.HasPrefix(target, "OLD."):
			col := strings.TrimPrefix(target, "OLD.")
			symTarget := "sym:" + schema + "." + triggerTarget + "." + col
			out = append(out, indexer.Reference{
				Kind:     store.EdgeKindBodyReferences,
				Source:   r.Source,
				Target:   symTarget,
				Metadata: map[string]any{"op": "read"},
			})
		}
		// Any other trigger_special target (e.g. bare "NEW"/"OLD") is dropped.
	}
	return out
}

// isStringLiteralExpr reports whether expr is a single SQL string literal and
// returns the unescaped inner content.  Recognises two forms:
//   - Single-quoted: 'INSERT INTO t VALUES (1)'  ('' = escaped quote inside)
//   - Dollar-quoted: $$INSERT INTO t$$ or $tag$INSERT INTO t$tag$
//
// Returns (false, "") for concatenations, variables, or other non-literal
// expressions.
func isStringLiteralExpr(expr string) (bool, string) {
	s := strings.TrimSpace(expr)

	// Single-quoted string literal.
	if len(s) >= 2 && s[0] == '\'' {
		i := 1
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i += 2 // SQL escaped quote ''
					continue
				}
				// Closing quote found — verify nothing follows.
				rest := strings.TrimSpace(s[i+1:])
				if rest != "" {
					return false, ""
				}
				inner := s[1:i]
				inner = strings.ReplaceAll(inner, "''", "'")
				return true, inner
			}
			i++
		}
		return false, ""
	}

	// Dollar-quoted string literal: $$...$$  or  $tag$...$tag$
	if len(s) >= 4 && s[0] == '$' {
		tagEnd := strings.Index(s[1:], "$")
		if tagEnd >= 0 {
			tag := s[0 : tagEnd+2] // e.g., "$$" or "$body$"
			if strings.HasSuffix(s, tag) && len(s) >= 2*len(tag) {
				inner := s[len(tag) : len(s)-len(tag)]
				// Reject if the closing tag appears inside (would indicate
				// multiple statements or a more complex expression).
				if !strings.Contains(inner, tag) {
					return true, inner
				}
			}
		}
	}

	return false, ""
}

// defaultSchemaFromFuncPath extracts the schema prefix from a function path
// like "public.testfunc" or "public.testfunc(int, text)".
func defaultSchemaFromFuncPath(funcPath string) string {
	if i := strings.Index(funcPath, "."); i >= 0 {
		return funcPath[:i]
	}
	return "public"
}

// markerRef emits a non-table body_references ref that signals a function-
// level property (uses_dynamic_sql, nested_execute).  The "dyn:" target
// prefix ensures graphTargetID drops the edge (no "sym:" prefix).
func markerRef(source, funcPath, flag string) indexer.Reference {
	return indexer.Reference{
		Kind:   store.EdgeKindBodyReferences,
		Source: source,
		Target: "dyn:" + funcPath,
		Metadata: map[string]any{
			"op":  "exec",
			flag: true,
		},
	}
}
