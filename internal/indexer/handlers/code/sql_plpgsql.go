package code

// sql_plpgsql.go — PL/pgSQL body walker for the SQL graph-pass handler.
//
// lib-o5dn.2: body-boundary composition + AST walker.
//
// Walks the PL/pgSQL AST produced by pg_query.ParsePlPgSqlToJSON and collects
// body_references Records (in-memory only). Edge emission to the store is
// lib-o5dn.3. Dynamic EXECUTE resolution is lib-o5dn.4. Trigger NEW/OLD
// resolution is lib-o5dn.4.
//
// Key invariants:
//   - Only called when LANGUAGE plpgsql is explicitly set (gates SIGABRT risk
//     described in lib-o5dn.1 spike, issue #129).
//   - On parse error: returns (nil, false); caller sets unit.Metadata["partial"]=true.
//   - EXECUTE nodes captured as-is with pending_execute=true.
//   - NEW/OLD trigger row fields captured with trigger_special=true.
//   - Edges are emitted to the store by the graph pass in indexer.go via
//     resolveBodyRefTarget + graphTargetID (lib-o5dn.3). pending_execute and
//     trigger_special refs (non-sym: targets) are skipped by graphTargetID and
//     deferred to lib-o5dn.4.

import (
	"encoding/json"
	"log/slog"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"librarian/internal/indexer"
	"librarian/internal/store"
)

// plpgsqlExtractRefs parses a PL/pgSQL function body and extracts in-memory
// body reference records. fullFuncSQL must be the complete CREATE FUNCTION ...
// LANGUAGE plpgsql statement (not just the body text).
//
// Returns (refs, true) on success or (nil, false) on parse error; callers
// should set unit.Metadata["partial"]=true when ok is false.
func plpgsqlExtractRefs(funcPath, defaultSchema, fullFuncSQL string) ([]indexer.Reference, bool) {
	return plpgsqlParseAndResolve(funcPath, defaultSchema, fullFuncSQL, "")
}

// plpgsqlParseAndResolve is the shared implementation used by plpgsqlExtractRefs
// and tests. triggerTarget is passed through to ResolveTriggerNewOld: when "", trigger_special
// refs are dropped (normal parse-time path); when non-empty, they are resolved
// to sym: paths against that table (used by tests to exercise the full pipeline).
func plpgsqlParseAndResolve(funcPath, defaultSchema, fullFuncSQL, triggerTarget string) ([]indexer.Reference, bool) {
	result, err := pg_query.ParsePlPgSqlToJSON(fullFuncSQL)
	if err != nil {
		slog.Debug("plpgsql body parse error", "func", funcPath, "err", err)
		return nil, false
	}

	var funcs []map[string]any
	if err := json.Unmarshal([]byte(result), &funcs); err != nil {
		slog.Debug("plpgsql JSON unmarshal error", "func", funcPath, "err", err)
		return nil, false
	}
	if len(funcs) == 0 {
		return nil, true
	}

	funcData, _ := funcs[0]["PLpgSQL_function"].(map[string]any)
	if funcData == nil {
		return nil, true
	}

	newVarno := plpgsqlIntField(funcData, "new_varno")
	oldVarno := plpgsqlIntField(funcData, "old_varno")
	isTrigger := newVarno > 0 || oldVarno > 0

	action, _ := funcData["action"].(map[string]any)
	if action == nil {
		return nil, true
	}

	var refs []indexer.Reference
	refs = append(refs, plpgsqlWalkStmt(action, funcPath, defaultSchema, isTrigger)...)

	if isTrigger {
		datums, _ := funcData["datums"].([]any)
		assignedDnos := plpgsqlScanAssignedDnos(action)
		refs = append(refs, plpgsqlTriggerFieldRefs(datums, newVarno, oldVarno, funcPath, assignedDnos)...)
	}

	refs = ResolveDynamicExecute(refs, nil)
	refs = ResolveTriggerNewOld(refs, triggerTarget, defaultSchema)

	return refs, true
}

// plpgsqlWalkStmt dispatches on the PLpgSQL JSON node-kind key.
func plpgsqlWalkStmt(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	for key, val := range stmt {
		data, _ := val.(map[string]any)
		if data == nil {
			continue
		}
		switch key {
		case "PLpgSQL_stmt_block":
			return plpgsqlWalkBlock(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_execsql":
			return plpgsqlWalkExecSQL(data, funcPath, defaultSchema)
		case "PLpgSQL_stmt_dynexecute":
			return plpgsqlWalkDynExecute(data, funcPath)
		case "PLpgSQL_stmt_if":
			return plpgsqlWalkIf(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_case":
			return plpgsqlWalkCase(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_fors":
			return plpgsqlWalkForS(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_fori":
			return plpgsqlWalkForI(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_while":
			return plpgsqlWalkWhile(data, funcPath, defaultSchema, isTrigger)
		case "PLpgSQL_stmt_call":
			return plpgsqlWalkCall(data, funcPath, defaultSchema)
		// PLpgSQL_stmt_return, PLpgSQL_stmt_raise, PLpgSQL_stmt_assign,
		// PLpgSQL_stmt_perform, PLpgSQL_stmt_exit, PLpgSQL_stmt_getdiag
		// do not touch tables — skip.
		}
	}
	return nil
}

func plpgsqlWalkStmtList(stmts []any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	var refs []indexer.Reference
	for _, s := range stmts {
		smap, _ := s.(map[string]any)
		if smap != nil {
			refs = append(refs, plpgsqlWalkStmt(smap, funcPath, defaultSchema, isTrigger)...)
		}
	}
	return refs
}

func plpgsqlWalkBlock(block map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	var refs []indexer.Reference

	body, _ := block["body"].([]any)
	refs = append(refs, plpgsqlWalkStmtList(body, funcPath, defaultSchema, isTrigger)...)

	// Walk exception handler bodies.
	if excNode, ok := block["exceptions"].(map[string]any); ok {
		if excBlock, ok := excNode["PLpgSQL_exception_block"].(map[string]any); ok {
			if excList, ok := excBlock["exc_list"].([]any); ok {
				for _, exc := range excList {
					excMap, _ := exc.(map[string]any)
					if excMap == nil {
						continue
					}
					handler, _ := excMap["PLpgSQL_exception"].(map[string]any)
					if handler == nil {
						continue
					}
					action, _ := handler["action"].([]any)
					refs = append(refs, plpgsqlWalkStmtList(action, funcPath, defaultSchema, isTrigger)...)
				}
			}
		}
	}
	return refs
}

func plpgsqlWalkExecSQL(stmt map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	sqlstmt, _ := stmt["sqlstmt"].(map[string]any)
	if sqlstmt == nil {
		return nil
	}
	plExpr, _ := sqlstmt["PLpgSQL_expr"].(map[string]any)
	if plExpr == nil {
		return nil
	}
	query, _ := plExpr["query"].(string)
	if query == "" {
		return nil
	}
	return plpgsqlParseSQLRefs(query, funcPath, defaultSchema)
}

// plpgsqlWalkDynExecute captures EXECUTE nodes with pending_execute=true.
// Actual resolution is lib-o5dn.4.
func plpgsqlWalkDynExecute(stmt map[string]any, funcPath string) []indexer.Reference {
	expr := plpgsqlGetExprQuery(stmt, "query")
	if expr == "" {
		expr = "<dynamic>"
	}
	return []indexer.Reference{{
		Kind:   store.EdgeKindBodyReferences,
		Source: "sym:" + funcPath,
		Target: expr, // raw EXECUTE expression — not a sym: path; resolved in lib-o5dn.4
		Metadata: map[string]any{
			"op":              "write",
			"pending_execute": true,
		},
	}}
}

func plpgsqlWalkIf(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	var refs []indexer.Reference

	if thenBody, ok := stmt["then_body"].([]any); ok {
		refs = append(refs, plpgsqlWalkStmtList(thenBody, funcPath, defaultSchema, isTrigger)...)
	}
	if elsifList, ok := stmt["elsif_list"].([]any); ok {
		for _, elsif := range elsifList {
			elsifMap, _ := elsif.(map[string]any)
			if elsifMap == nil {
				continue
			}
			if elsifItem, ok := elsifMap["PLpgSQL_if_elsif"].(map[string]any); ok {
				if stmts, ok := elsifItem["stmts"].([]any); ok {
					refs = append(refs, plpgsqlWalkStmtList(stmts, funcPath, defaultSchema, isTrigger)...)
				}
			}
		}
	}
	if elseBody, ok := stmt["else_body"].([]any); ok {
		refs = append(refs, plpgsqlWalkStmtList(elseBody, funcPath, defaultSchema, isTrigger)...)
	}
	return refs
}

func plpgsqlWalkCase(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	var refs []indexer.Reference

	if cwList, ok := stmt["case_when_list"].([]any); ok {
		for _, cw := range cwList {
			cwMap, _ := cw.(map[string]any)
			if cwMap == nil {
				continue
			}
			if cwItem, ok := cwMap["PLpgSQL_case_when"].(map[string]any); ok {
				if stmts, ok := cwItem["stmts"].([]any); ok {
					refs = append(refs, plpgsqlWalkStmtList(stmts, funcPath, defaultSchema, isTrigger)...)
				}
			}
		}
	}
	if elseStmts, ok := stmt["else_stmts"].([]any); ok {
		refs = append(refs, plpgsqlWalkStmtList(elseStmts, funcPath, defaultSchema, isTrigger)...)
	}
	return refs
}

func plpgsqlWalkForS(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	var refs []indexer.Reference

	// The SELECT query produces read references.
	if queryNode, ok := stmt["query"].(map[string]any); ok {
		if plExpr, ok := queryNode["PLpgSQL_expr"].(map[string]any); ok {
			if q, ok := plExpr["query"].(string); ok && q != "" {
				refs = append(refs, plpgsqlParseSQLRefs(q, funcPath, defaultSchema)...)
			}
		}
	}
	if body, ok := stmt["body"].([]any); ok {
		refs = append(refs, plpgsqlWalkStmtList(body, funcPath, defaultSchema, isTrigger)...)
	}
	return refs
}

func plpgsqlWalkForI(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	if body, ok := stmt["body"].([]any); ok {
		return plpgsqlWalkStmtList(body, funcPath, defaultSchema, isTrigger)
	}
	return nil
}

func plpgsqlWalkWhile(stmt map[string]any, funcPath, defaultSchema string, isTrigger bool) []indexer.Reference {
	if body, ok := stmt["body"].([]any); ok {
		return plpgsqlWalkStmtList(body, funcPath, defaultSchema, isTrigger)
	}
	return nil
}

// plpgsqlWalkCall handles PLpgSQL_stmt_call (CALL procedure).
func plpgsqlWalkCall(stmt map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	query := plpgsqlGetExprQuery(stmt, "expr")
	if query == "" {
		return nil
	}
	target := plpgsqlResolveCallTarget(query, defaultSchema)
	if target == "" {
		return nil
	}
	return []indexer.Reference{plpgsqlBodyRef(funcPath, target, "procedure_call", false, false)}
}

// plpgsqlGetExprQuery extracts query text from stmt[fieldName].PLpgSQL_expr.query.
func plpgsqlGetExprQuery(stmt map[string]any, fieldName string) string {
	exprNode, _ := stmt[fieldName].(map[string]any)
	if exprNode == nil {
		return ""
	}
	plExpr, _ := exprNode["PLpgSQL_expr"].(map[string]any)
	if plExpr == nil {
		return ""
	}
	q, _ := plExpr["query"].(string)
	return q
}

// plpgsqlResolveCallTarget tries to extract a canonical "schema.name" procedure
// path from a CALL expression string. Returns "" on failure.
func plpgsqlResolveCallTarget(query, defaultSchema string) string {
	// PLpgSQL_expr.query for PLpgSQL_stmt_call is the full CALL statement
	// ("CALL proc(args)"). Strip the keyword, then extract the callee identifier.
	trimmed := strings.TrimSpace(query)
	if strings.HasPrefix(strings.ToUpper(trimmed), "CALL ") {
		trimmed = strings.TrimSpace(trimmed[5:])
	}
	return plpgsqlExtractFirstIdent(trimmed, defaultSchema)
}

// plpgsqlFuncCallName extracts "schema.name" from a FuncCall JSON node.
func plpgsqlFuncCallName(funccall any, defaultSchema string) string {
	fc, _ := funccall.(map[string]any)
	if fc == nil {
		return ""
	}
	// pg_query wraps FuncCall nodes as {"FuncCall": {...}} in expression contexts;
	// unwrap if present so funcname is accessible at the right level.
	if inner, ok := fc["FuncCall"].(map[string]any); ok {
		fc = inner
	}
	funcname, _ := fc["funcname"].([]any)
	var parts []string
	for _, n := range funcname {
		nmap, _ := n.(map[string]any)
		if nmap == nil {
			continue
		}
		if strNode, ok := nmap["String"].(map[string]any); ok {
			if sval, ok := strNode["sval"].(string); ok && sval != "" {
				parts = append(parts, sval)
			}
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return defaultSchema + "." + parts[0]
	default:
		return strings.Join(parts, ".")
	}
}

// plpgsqlExtractFirstIdent returns "schema.name" from a "name(..." or
// "schema.name(..." expression. Best-effort fallback when pg_query parsing fails.
func plpgsqlExtractFirstIdent(expr, defaultSchema string) string {
	name := expr
	if i := strings.Index(name, "("); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	if name == "" {
		return ""
	}
	if !strings.Contains(name, ".") {
		return defaultSchema + "." + name
	}
	return name
}

// plpgsqlTriggerFieldRefs emits body_references for NEW/OLD record field
// accesses found in the datums array. lib-o5dn.4 resolves these to the actual
// trigger table; this bead only captures them with trigger_special=true.
//
// assignedDnos is the set of datum numbers that appear on the LHS of a
// PLpgSQL_stmt_assign in the function body (collected by plpgsqlScanAssignedDnos).
// The datum number (dno) for each entry is its array index — pg_query's JSON
// omits an explicit "dno" field for PLpgSQL_recfield nodes.
// NEW recfields whose dno is in assignedDnos get context="assignment" so that
// ResolveTriggerNewOld can emit op=write for those refs (lib-uer8).
func plpgsqlTriggerFieldRefs(datums []any, newVarno, oldVarno int, funcPath string, assignedDnos map[int]bool) []indexer.Reference {
	var refs []indexer.Reference
	for i, d := range datums {
		dmap, _ := d.(map[string]any)
		if dmap == nil {
			continue
		}
		recfield, ok := dmap["PLpgSQL_recfield"].(map[string]any)
		if !ok {
			continue
		}
		fieldname, _ := recfield["fieldname"].(string)
		if fieldname == "" {
			continue
		}
		recparentno := plpgsqlIntField(recfield, "recparentno")
		dno := i // datum number is the array index; pg_query JSON omits explicit "dno" for recfields

		var target string
		switch {
		case newVarno > 0 && recparentno == newVarno:
			target = "NEW." + fieldname
		case oldVarno > 0 && recparentno == oldVarno:
			target = "OLD." + fieldname
		default:
			continue
		}
		meta := map[string]any{
			"op":              "read",
			"trigger_special": true,
		}
		if recparentno == newVarno && assignedDnos[dno] {
			meta["context"] = "assignment"
		}
		refs = append(refs, indexer.Reference{
			Kind:     store.EdgeKindBodyReferences,
			Source:   "sym:" + funcPath,
			Target:   target, // resolved in lib-o5dn.4
			Metadata: meta,
		})
	}
	return refs
}

// plpgsqlScanAssignedDnos scans a top-level stmt node (the action field of a
// PLpgSQL_function) and returns the set of varno values that appear as the LHS
// of PLpgSQL_stmt_assign nodes anywhere in the body.
func plpgsqlScanAssignedDnos(action map[string]any) map[int]bool {
	assigned := make(map[int]bool)
	plpgsqlScanAssignStmt(action, assigned)
	return assigned
}

func plpgsqlScanAssignStmt(stmt map[string]any, assigned map[int]bool) {
	for key, val := range stmt {
		data, _ := val.(map[string]any)
		if data == nil {
			continue
		}
		switch key {
		case "PLpgSQL_stmt_assign":
			if varno := plpgsqlIntField(data, "varno"); varno > 0 {
				assigned[varno] = true
			}
		case "PLpgSQL_stmt_block":
			body, _ := data["body"].([]any)
			plpgsqlScanAssignStmtList(body, assigned)
			if excNode, ok := data["exceptions"].(map[string]any); ok {
				if excBlock, ok := excNode["PLpgSQL_exception_block"].(map[string]any); ok {
					if excList, ok := excBlock["exc_list"].([]any); ok {
						for _, exc := range excList {
							excMap, _ := exc.(map[string]any)
							if excMap == nil {
								continue
							}
							handler, _ := excMap["PLpgSQL_exception"].(map[string]any)
							if handler == nil {
								continue
							}
							handlerAction, _ := handler["action"].([]any)
							plpgsqlScanAssignStmtList(handlerAction, assigned)
						}
					}
				}
			}
		case "PLpgSQL_stmt_if":
			if thenBody, ok := data["then_body"].([]any); ok {
				plpgsqlScanAssignStmtList(thenBody, assigned)
			}
			if elsifList, ok := data["elsif_list"].([]any); ok {
				for _, elsif := range elsifList {
					elsifMap, _ := elsif.(map[string]any)
					if elsifMap == nil {
						continue
					}
					if elsifItem, ok := elsifMap["PLpgSQL_if_elsif"].(map[string]any); ok {
						if stmts, ok := elsifItem["stmts"].([]any); ok {
							plpgsqlScanAssignStmtList(stmts, assigned)
						}
					}
				}
			}
			if elseBody, ok := data["else_body"].([]any); ok {
				plpgsqlScanAssignStmtList(elseBody, assigned)
			}
		case "PLpgSQL_stmt_case":
			if cwList, ok := data["case_when_list"].([]any); ok {
				for _, cw := range cwList {
					cwMap, _ := cw.(map[string]any)
					if cwMap == nil {
						continue
					}
					if cwItem, ok := cwMap["PLpgSQL_case_when"].(map[string]any); ok {
						if stmts, ok := cwItem["stmts"].([]any); ok {
							plpgsqlScanAssignStmtList(stmts, assigned)
						}
					}
				}
			}
			if elseStmts, ok := data["else_stmts"].([]any); ok {
				plpgsqlScanAssignStmtList(elseStmts, assigned)
			}
		case "PLpgSQL_stmt_fors", "PLpgSQL_stmt_fori", "PLpgSQL_stmt_while",
			"PLpgSQL_stmt_loop", "PLpgSQL_stmt_foreach_a":
			if body, ok := data["body"].([]any); ok {
				plpgsqlScanAssignStmtList(body, assigned)
			}
		}
	}
}

func plpgsqlScanAssignStmtList(stmts []any, assigned map[int]bool) {
	for _, s := range stmts {
		smap, _ := s.(map[string]any)
		if smap != nil {
			plpgsqlScanAssignStmt(smap, assigned)
		}
	}
}

// plpgsqlBodyRef creates a body_references Reference with a sym: target.
func plpgsqlBodyRef(funcPath, targetPath, op string, triggerSpecial, pendingExecute bool) indexer.Reference {
	meta := map[string]any{"op": op}
	if triggerSpecial {
		meta["trigger_special"] = true
	}
	if pendingExecute {
		meta["pending_execute"] = true
	}
	return indexer.Reference{
		Kind:     store.EdgeKindBodyReferences,
		Source:   "sym:" + funcPath,
		Target:   "sym:" + targetPath,
		Metadata: meta,
	}
}

// plpgsqlParseSQLRefs parses a SQL statement string (parseMode=0) and extracts
// table references using the regular pg_query SQL parser.
func plpgsqlParseSQLRefs(query, funcPath, defaultSchema string) []indexer.Reference {
	result, err := pg_query.ParseToJSON(query)
	if err != nil {
		slog.Debug("plpgsql: SQL stmt parse error", "func", funcPath, "err", err)
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return nil
	}
	stmts, _ := parsed["stmts"].([]any)
	var refs []indexer.Reference
	for _, s := range stmts {
		smap, _ := s.(map[string]any)
		if smap == nil {
			continue
		}
		stmtNode, _ := smap["stmt"].(map[string]any)
		if stmtNode == nil {
			continue
		}
		refs = append(refs, plpgsqlExtractSQLStmtRefs(stmtNode, funcPath, defaultSchema)...)
	}
	return refs
}

// plpgsqlExtractSQLStmtRefs dispatches on the pg_query statement kind.
func plpgsqlExtractSQLStmtRefs(stmt map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	for kind, val := range stmt {
		data, _ := val.(map[string]any)
		if data == nil {
			continue
		}
		switch kind {
		case "SelectStmt":
			return plpgsqlExtractSelectRefs(data, funcPath, defaultSchema)
		case "InsertStmt":
			return plpgsqlExtractInsertRefs(data, funcPath, defaultSchema)
		case "UpdateStmt":
			return plpgsqlExtractUpdateRefs(data, funcPath, defaultSchema)
		case "DeleteStmt":
			return plpgsqlExtractDeleteRefs(data, funcPath, defaultSchema)
		case "MergeStmt":
			return plpgsqlExtractMergeRefs(data, funcPath, defaultSchema)
		case "CallStmt":
			if name := plpgsqlFuncCallName(data["funccall"], defaultSchema); name != "" {
				return []indexer.Reference{plpgsqlBodyRef(funcPath, name, "procedure_call", false, false)}
			}
		}
	}
	return nil
}

func plpgsqlExtractSelectRefs(data map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	fromClause, _ := data["fromClause"].([]any)
	return plpgsqlExtractFromClause(fromClause, funcPath, defaultSchema, "read")
}

func plpgsqlExtractInsertRefs(data map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	var refs []indexer.Reference

	// Target relation is a direct RangeVar (not Node-wrapped) in InsertStmt.
	if relMap, ok := data["relation"].(map[string]any); ok {
		if path := plpgsqlRVDirectPath(relMap, defaultSchema); path != "" {
			refs = append(refs, plpgsqlBodyRef(funcPath, path, "write", false, false))
		}
	}
	// Source tables in INSERT...SELECT.
	if selectNode, ok := data["selectStmt"].(map[string]any); ok {
		if ss, ok := selectNode["SelectStmt"].(map[string]any); ok {
			refs = append(refs, plpgsqlExtractSelectRefs(ss, funcPath, defaultSchema)...)
		}
	}
	return refs
}

func plpgsqlExtractUpdateRefs(data map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	var refs []indexer.Reference

	if relMap, ok := data["relation"].(map[string]any); ok {
		if path := plpgsqlRVDirectPath(relMap, defaultSchema); path != "" {
			refs = append(refs, plpgsqlBodyRef(funcPath, path, "write", false, false))
		}
	}
	fromClause, _ := data["fromClause"].([]any)
	refs = append(refs, plpgsqlExtractFromClause(fromClause, funcPath, defaultSchema, "read")...)
	return refs
}

func plpgsqlExtractDeleteRefs(data map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	var refs []indexer.Reference

	if relMap, ok := data["relation"].(map[string]any); ok {
		if path := plpgsqlRVDirectPath(relMap, defaultSchema); path != "" {
			refs = append(refs, plpgsqlBodyRef(funcPath, path, "write", false, false))
		}
	}
	usingClause, _ := data["usingClause"].([]any)
	refs = append(refs, plpgsqlExtractFromClause(usingClause, funcPath, defaultSchema, "read")...)
	return refs
}

func plpgsqlExtractMergeRefs(data map[string]any, funcPath, defaultSchema string) []indexer.Reference {
	var refs []indexer.Reference

	if relMap, ok := data["relation"].(map[string]any); ok {
		if path := plpgsqlRVDirectPath(relMap, defaultSchema); path != "" {
			refs = append(refs, plpgsqlBodyRef(funcPath, path, "both", false, false))
		}
	}
	if srcNode, ok := data["sourceRelation"].(map[string]any); ok {
		refs = append(refs, plpgsqlExtractFromClauseItem(srcNode, funcPath, defaultSchema, "read")...)
	}
	return refs
}

// plpgsqlExtractFromClause extracts table references from a FROM/USING clause array.
// Each element is a Node-wrapped RangeVar or JoinExpr.
func plpgsqlExtractFromClause(items []any, funcPath, defaultSchema, op string) []indexer.Reference {
	var refs []indexer.Reference
	for _, item := range items {
		imap, _ := item.(map[string]any)
		if imap == nil {
			continue
		}
		refs = append(refs, plpgsqlExtractFromClauseItem(imap, funcPath, defaultSchema, op)...)
	}
	return refs
}

// plpgsqlExtractFromClauseItem handles one Node-wrapped FROM item.
func plpgsqlExtractFromClauseItem(item map[string]any, funcPath, defaultSchema, op string) []indexer.Reference {
	// Node is wrapped as {"RangeVar": {...}} or {"JoinExpr": {...}}.
	if rvNode, ok := item["RangeVar"].(map[string]any); ok {
		if path := plpgsqlRVDirectPath(rvNode, defaultSchema); path != "" {
			return []indexer.Reference{plpgsqlBodyRef(funcPath, path, op, false, false)}
		}
	}
	if joinExpr, ok := item["JoinExpr"].(map[string]any); ok {
		var refs []indexer.Reference
		if larg, ok := joinExpr["larg"].(map[string]any); ok {
			refs = append(refs, plpgsqlExtractFromClauseItem(larg, funcPath, defaultSchema, op)...)
		}
		if rarg, ok := joinExpr["rarg"].(map[string]any); ok {
			refs = append(refs, plpgsqlExtractFromClauseItem(rarg, funcPath, defaultSchema, op)...)
		}
		return refs
	}
	return nil
}

// plpgsqlRVDirectPath builds "schema.table" from a direct (non-Node-wrapped)
// RangeVar map. Used for InsertStmt.relation, UpdateStmt.relation, etc.
func plpgsqlRVDirectPath(rv map[string]any, defaultSchema string) string {
	relname, _ := rv["relname"].(string)
	if relname == "" {
		return ""
	}
	schema, _ := rv["schemaname"].(string)
	if schema == "" {
		schema = defaultSchema
	}
	return schema + "." + relname
}

// plpgsqlIntField extracts an integer from a JSON map value (pg_query JSON
// uses float64 for all numbers after json.Unmarshal).
func plpgsqlIntField(m map[string]any, key string) int {
	switch n := m[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
