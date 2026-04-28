package code

// sql.go — graph-pass Grammar for .sql / .psql / .ddl files (v2, lib-do0).
//
// The v1 SQL handler (internal/indexer/handlers/sql/) handles the docs pass
// (chunking, search). This grammar handles the structural pass: it extracts
// symbol Units (tables, columns, indexes, views, functions, sequences, schemas)
// and FK `references` edges from DDL statements, feeding the graph layer so
// queries like `neighbors sym:public.orders` return the right answers.
//
// Grammar: github.com/DerekStride/tree-sitter-sql v0.3.11 (ABI 15), vendored
// at internal/indexer/handlers/code/tree_sitter_sql/ because the module zip
// omits the generated src/parser.c.
//
// # Design decisions (lib-do0)
//
// Unit.Path canonical form: `schema.table[.column]`. Default schema is "public"
// when unqualified. Indexes, views, functions, sequences are `schema.name`.
// Functions with overloads use `schema.name(arg_types)` for disambiguation.
//
// The shared extractUnits walker is bypassed (SymbolKinds/ContainerKinds return
// empty maps). Instead, PostProcess builds all Units and References directly
// from the AST — necessary because SQL paths are schema-qualified absolute names
// (e.g. "public.users") and the shared walker would prepend the file stem, which
// would produce wrong canonical paths.
//
// FK edges use EdgeKindReferences ("references"): child column/table → parent.
// Edge direction mirrors inherits: child → parent.
//
// psql meta-commands (\timing on, \c database, etc.) produce ERROR nodes in the
// grammar. The handler skips all top-level ERROR nodes — there is no valid DDL
// statement that the grammar emits as ERROR, so skipping them entirely is safe
// and avoids surfacing parse noise for meta-command-heavy migration files.
//
// Unit.Kind values emitted:
//   - "table"    — CREATE TABLE
//   - "column"   — column inside a table
//   - "index"    — CREATE [UNIQUE] INDEX
//   - "view"     — CREATE [MATERIALIZED] VIEW
//   - "function" — CREATE FUNCTION / CREATE PROCEDURE
//   - "sequence" — CREATE SEQUENCE
//   - "schema"   — CREATE SCHEMA
import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code/tree_sitter_sql"
	"librarian/internal/store"
)

// SQLGrammar implements Grammar for SQL DDL files (Postgres dialect).
type SQLGrammar struct{}

// NewSQLGrammar returns the SQL grammar implementation.
func NewSQLGrammar() *SQLGrammar { return &SQLGrammar{} }

func (*SQLGrammar) Name() string               { return "sql_graph" }
func (*SQLGrammar) Extensions() []string       { return []string{".sql", ".psql", ".ddl"} }
func (*SQLGrammar) Language() *sitter.Language { return sitter.NewLanguage(tree_sitter_sql.Language()) }
func (*SQLGrammar) CommentNodeTypes() []string { return []string{"comment", "marginalia"} }
func (*SQLGrammar) DocstringFromNode(_ *sitter.Node, _ []byte) string { return "" }

// PackageName returns "" — SQL files have no package declaration. The shared
// walker falls back to file-stem, but PostProcess bypasses walker output
// entirely and rebuilds Units with schema-qualified absolute paths.
func (*SQLGrammar) PackageName(_ *sitter.Node, _ []byte) string { return "" }

// SymbolKinds returns an empty map. Unit extraction is handled entirely in
// PostProcess to produce schema-qualified canonical paths.
func (*SQLGrammar) SymbolKinds() map[string]string { return map[string]string{} }

// ContainerKinds returns an empty map (matching SymbolKinds).
func (*SQLGrammar) ContainerKinds() map[string]bool { return map[string]bool{} }

// SymbolName is unreachable (SymbolKinds is empty) but must satisfy the interface.
func (*SQLGrammar) SymbolName(_ *sitter.Node, _ []byte) string { return "" }

// Imports returns nil — SQL has no import directives. FK edges are emitted as
// References by PostProcess, not via the import channel.
func (*SQLGrammar) Imports(_ *sitter.Node, _ []byte) []ImportRef { return nil }

// Compile-time assertion.
var _ parsedDocPostProcessor = (*SQLGrammar)(nil)

// PostProcess is the primary extraction point for SQL Units and FK References.
// It runs after the (empty) shared walker pass, so doc.Units is empty on entry.
// PostProcess rebuilds it from the AST with schema-qualified paths.
func (g *SQLGrammar) PostProcess(doc *indexer.ParsedDoc, root *sitter.Node, source []byte) {
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	if tool := detectMigrationTool(doc.Path, source); tool != "" {
		doc.Metadata["migration_tool"] = tool
	}
	sqlExtractAll(root, source, doc)
}

// sqlExtractAll walks the parsed program and extracts all DDL declarations.
func sqlExtractAll(root *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	migTool, _ := doc.Metadata["migration_tool"].(string)

	for i := uint(0); i < root.NamedChildCount(); i++ {
		stmt := root.NamedChild(i)
		if stmt == nil {
			continue
		}
		// Top-level `statement` wrapper — descend one level.
		if stmt.Kind() == "statement" {
			if stmt.NamedChildCount() == 0 {
				continue
			}
			stmt = stmt.NamedChild(0)
			if stmt == nil {
				continue
			}
		}
		// Skip ERROR nodes (e.g. psql meta-commands like \timing on).
		if stmt.Kind() == "ERROR" {
			continue
		}

		switch stmt.Kind() {
		case "create_table":
			sqlExtractTable(stmt, source, doc, migTool)
		case "create_index":
			sqlExtractIndex(stmt, source, doc, migTool)
		case "create_view":
			sqlExtractView(stmt, source, doc, false, migTool)
		case "create_materialized_view":
			sqlExtractView(stmt, source, doc, true, migTool)
		case "create_function":
			// Note: CREATE PROCEDURE is not supported by the DerekStride grammar
			// (v0.3.11 emits ERROR for it). Procedure extraction deferred to a
			// future grammar update.
			sqlExtractFunction(stmt, source, doc, migTool)
		case "create_sequence":
			sqlExtractSequence(stmt, source, doc, migTool)
		case "create_schema":
			sqlExtractSchema(stmt, source, doc, migTool)
		case "alter_table":
			sqlExtractAlterTableFKs(stmt, source, doc)
		}
	}
}

// sqlObjectRef resolves an `object_reference` node to (schema, name). If the
// reference has two identifiers, they are schema+name; if one, schema defaults
// to "public".
func sqlObjectRef(n *sitter.Node, source []byte) (schema, name string) {
	var idents []string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			idents = append(idents, strings.TrimSpace(c.Utf8Text(source)))
		}
	}
	switch len(idents) {
	case 0:
		return "public", ""
	case 1:
		return "public", idents[0]
	default:
		return idents[0], idents[1]
	}
}

// sqlQualifiedPath returns "schema.name" from an object_reference node.
func sqlQualifiedPath(n *sitter.Node, source []byte) string {
	schema, name := sqlObjectRef(n, source)
	if name == "" {
		return schema
	}
	return schema + "." + name
}

// sqlFindChild returns the first named child with the given kind, or nil.
func sqlFindChild(n *sitter.Node, kind string) *sitter.Node {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == kind {
			return c
		}
	}
	return nil
}

// sqlExtractTable extracts a CREATE TABLE statement.
func sqlExtractTable(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, migTool string) {
	objRef := sqlFindChild(n, "object_reference")
	if objRef == nil {
		return
	}
	schema, tableName := sqlObjectRef(objRef, source)
	if tableName == "" {
		return
	}
	tablePath := schema + "." + tableName

	meta := map[string]any{
		"schema": schema,
		"name":   tableName,
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	// Append the table unit before column units so parent precedes children
	// in doc.Units, which matches the order invariant used by AssertGrammarInvariants
	// and makes the graph-pass projection order predictable.
	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "table",
		Title:    tableName,
		Path:     tablePath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})

	colDefs := sqlFindChild(n, "column_definitions")
	if colDefs != nil {
		var colNames []string
		var lastColPath string
		for i := uint(0); i < colDefs.NamedChildCount(); i++ {
			child := colDefs.NamedChild(i)
			if child == nil {
				continue
			}
			switch child.Kind() {
			case "column_definition":
				col := sqlExtractColumn(child, source, tablePath, migTool, doc)
				if col != nil {
					colNames = append(colNames, col.Title)
					lastColPath = col.Path
					doc.Units = append(doc.Units, *col)
				}
			case "constraints":
				// Table-level FK constraints.
				sqlExtractTableConstraintFKs(child, source, tablePath, doc)
			case "ERROR":
				// Some inline REFERENCES forms (e.g. REFERENCES tbl without parens)
				// parse as an ERROR node sibling to the preceding column_definition.
				// Emit a best-effort FK ref anchored at the previous column.
				if lastColPath != "" {
					sqlExtractErrorNodeFK(child, source, lastColPath, doc)
				}
			}
		}
		if len(colNames) > 0 {
			// Update the table unit's metadata with the collected column names.
			// The unit was already appended; mutate its Metadata map directly
			// since map values are reference types.
			meta["columns"] = colNames
		}
	}
}

// sqlExtractColumn extracts a column_definition node inside a CREATE TABLE.
// Also emits inline FK References when the column includes a REFERENCES clause.
func sqlExtractColumn(n *sitter.Node, source []byte, tablePath string, migTool string, doc *indexer.ParsedDoc) *indexer.Unit {
	// First identifier is the column name.
	var colName string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			colName = strings.TrimSpace(c.Utf8Text(source))
			break
		}
	}
	if colName == "" {
		return nil
	}
	colPath := tablePath + "." + colName

	// Detect column type.
	colType := sqlColumnType(n, source)

	// Detect NOT NULL, PRIMARY KEY.
	notNull := false
	primaryKey := false
	hasDefault := false
	defaultVal := ""
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "keyword_not":
			notNull = true
		case "keyword_primary":
			primaryKey = true
		case "keyword_default":
			// Next named sibling after keyword_default is the default value.
			hasDefault = true
			if i+1 < n.NamedChildCount() {
				next := n.NamedChild(i + 1)
				if next != nil {
					defaultVal = strings.TrimSpace(next.Utf8Text(source))
				}
			}
		}
	}

	meta := map[string]any{
		"type":        colType,
		"nullable":    !notNull && !primaryKey,
		"primary_key": primaryKey,
	}
	if hasDefault {
		meta["default"] = defaultVal
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	// Emit table→column contains edge so `neighbors sym:public.users --direction=out`
	// returns the column nodes.
	doc.Refs = append(doc.Refs, indexer.Reference{
		Kind:   store.EdgeKindContains,
		Source: tablePath,
		Target: colPath,
		Loc:    nodeLocation(n),
	})

	// Emit inline FK reference: REFERENCES target_table(target_col)
	sqlExtractInlineFK(n, source, colPath, doc)

	return &indexer.Unit{
		Kind:     "column",
		Title:    colName,
		Path:     colPath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	}
}

// sqlColumnType extracts a human-readable type string from a column_definition.
// The grammar uses keyword_* nodes (keyword_text, keyword_int, etc.) and
// wrapper nodes (int, numeric, character_varying, …). We grab the first
// type-ish named child, walking one level of nesting.
func sqlColumnType(n *sitter.Node, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		k := c.Kind()
		// Skip the column name identifier and constraint keywords.
		if k == "identifier" || strings.HasPrefix(k, "keyword_primary") ||
			strings.HasPrefix(k, "keyword_not") || strings.HasPrefix(k, "keyword_null") ||
			strings.HasPrefix(k, "keyword_default") || strings.HasPrefix(k, "keyword_check") ||
			strings.HasPrefix(k, "keyword_unique") || strings.HasPrefix(k, "keyword_references") {
			continue
		}
		if strings.HasPrefix(k, "keyword_") {
			return strings.ToLower(strings.TrimPrefix(k, "keyword_"))
		}
		// Wrapper type node (int, numeric, character_varying, …):
		// Its text is the raw SQL type string.
		txt := strings.TrimSpace(c.Utf8Text(source))
		if txt != "" {
			return txt
		}
	}
	return ""
}

// sqlExtractInlineFK checks a column_definition for an inline REFERENCES clause
// and emits a Reference{Kind:"references"} if found.
func sqlExtractInlineFK(n *sitter.Node, source []byte, colPath string, doc *indexer.ParsedDoc) {
	hasRef := false
	var targetRef *sitter.Node
	var targetColIdent string

	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "keyword_references" {
			hasRef = true
			// Next named children: object_reference (target table), then optional identifier (target column).
			for j := i + 1; j < n.NamedChildCount(); j++ {
				nc := n.NamedChild(j)
				if nc == nil {
					continue
				}
				if nc.Kind() == "object_reference" && targetRef == nil {
					targetRef = nc
				} else if nc.Kind() == "identifier" && targetRef != nil && targetColIdent == "" {
					targetColIdent = strings.TrimSpace(nc.Utf8Text(source))
				}
			}
			break
		}
	}

	if !hasRef || targetRef == nil {
		return
	}
	targetSchema, targetTable := sqlObjectRef(targetRef, source)
	if targetTable == "" {
		return
	}
	target := targetSchema + "." + targetTable
	if targetColIdent != "" {
		target = target + "." + targetColIdent
	}

	doc.Refs = append(doc.Refs, indexer.Reference{
		Kind:   store.EdgeKindReferences,
		Source: colPath,
		Target: target,
		Loc:    nodeLocation(n),
	})
}

// sqlExtractTableConstraintFKs walks a `constraints` node inside column_definitions
// and emits References for FOREIGN KEY constraints.
func sqlExtractTableConstraintFKs(n *sitter.Node, source []byte, tablePath string, doc *indexer.ParsedDoc) {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "constraint" {
			continue
		}
		sqlExtractConstraintFK(c, source, tablePath, doc)
	}
}

// sqlExtractErrorNodeFK handles ERROR nodes that contain an inline REFERENCES
// clause whose column list was omitted (e.g. `col INT REFERENCES other`). The
// grammar produces an ERROR sibling in column_definitions when the REFERENCES
// target lacks parentheses. We emit a best-effort FK ref anchored at colPath.
func sqlExtractErrorNodeFK(n *sitter.Node, source []byte, colPath string, doc *indexer.ParsedDoc) {
	hasRef := false
	var targetRef *sitter.Node
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "keyword_references" {
			hasRef = true
			continue
		}
		if hasRef && c.Kind() == "object_reference" && targetRef == nil {
			targetRef = c
		}
	}
	if !hasRef || targetRef == nil {
		return
	}
	targetSchema, targetTable := sqlObjectRef(targetRef, source)
	if targetTable == "" {
		return
	}
	doc.Refs = append(doc.Refs, indexer.Reference{
		Kind:   store.EdgeKindReferences,
		Source: colPath,
		Target: targetSchema + "." + targetTable,
		Loc:    nodeLocation(n),
	})
}

// sqlExtractConstraintFK parses a single `constraint` node for FK information.
// Handles both table-level (FOREIGN KEY (cols) REFERENCES ...) and ALTER TABLE
// constraint forms.
func sqlExtractConstraintFK(n *sitter.Node, source []byte, sourcePath string, doc *indexer.ParsedDoc) {
	// Check if this is a FK constraint (has keyword_foreign or starts with KEY).
	isFKConstraint := false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && (c.Kind() == "keyword_foreign" || c.Kind() == "keyword_key") {
			isFKConstraint = true
			break
		}
	}
	if !isFKConstraint {
		return
	}

	// Extract source columns from ordered_columns.
	var sourceCols []string
	if oc := sqlFindChild(n, "ordered_columns"); oc != nil {
		for i := uint(0); i < oc.NamedChildCount(); i++ {
			col := oc.NamedChild(i)
			if col == nil || col.Kind() != "column" {
				continue
			}
			id := sqlFindChild(col, "identifier")
			if id != nil {
				sourceCols = append(sourceCols, strings.TrimSpace(id.Utf8Text(source)))
			}
		}
	}

	// Find REFERENCES target.
	var targetRef *sitter.Node
	var targetColIdent string
	seenRefs := false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "keyword_references" {
			seenRefs = true
			continue
		}
		if seenRefs {
			if c.Kind() == "object_reference" && targetRef == nil {
				targetRef = c
			} else if c.Kind() == "identifier" && targetRef != nil && targetColIdent == "" {
				targetColIdent = strings.TrimSpace(c.Utf8Text(source))
			}
		}
	}
	if targetRef == nil {
		return
	}
	targetSchema, targetTable := sqlObjectRef(targetRef, source)
	if targetTable == "" {
		return
	}

	// Extract ON DELETE / ON UPDATE actions.
	onDelete, onUpdate := sqlConstraintActions(n, source)
	meta := map[string]any{}
	if onDelete != "" {
		meta["on_delete"] = onDelete
	}
	if onUpdate != "" {
		meta["on_update"] = onUpdate
	}

	// Emit one Reference per source column (or one at table level if no columns).
	if len(sourceCols) == 0 {
		target := targetSchema + "." + targetTable
		if targetColIdent != "" {
			target += "." + targetColIdent
		}
		ref := indexer.Reference{
			Kind:   store.EdgeKindReferences,
			Source: sourcePath,
			Target: target,
			Loc:    nodeLocation(n),
		}
		if len(meta) > 0 {
			ref.Metadata = meta
		}
		doc.Refs = append(doc.Refs, ref)
		return
	}

	// One ref per source column. Target column: if there's a single target column,
	// use it; otherwise use table-level target.
	for _, sc := range sourceCols {
		sourceColPath := sourcePath + "." + sc
		target := targetSchema + "." + targetTable
		if targetColIdent != "" {
			target += "." + targetColIdent
		}
		ref := indexer.Reference{
			Kind:   store.EdgeKindReferences,
			Source: sourceColPath,
			Target: target,
			Loc:    nodeLocation(n),
		}
		if len(meta) > 0 {
			ref.Metadata = meta
		}
		doc.Refs = append(doc.Refs, ref)
	}
}

// sqlConstraintActions scans a constraint node for ON DELETE / ON UPDATE
// referential action keywords and returns the action strings.
func sqlConstraintActions(n *sitter.Node, source []byte) (onDelete, onUpdate string) {
	// Walk all named children; when we see keyword_on, look at the following
	// keyword (delete/update) and then the action keyword.
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() != "keyword_on" {
			continue
		}
		if i+1 >= n.NamedChildCount() {
			continue
		}
		trigger := n.NamedChild(i + 1)
		if trigger == nil {
			continue
		}
		action := sqlRefAction(n, source, i+2)
		switch trigger.Kind() {
		case "keyword_delete":
			onDelete = action
		case "keyword_update":
			onUpdate = action
		}
	}
	return
}

// sqlRefAction reads the referential action starting at index start in n's
// named children. Returns a normalised string like "CASCADE", "SET NULL", etc.
func sqlRefAction(n *sitter.Node, source []byte, start uint) string {
	if start >= n.NamedChildCount() {
		return ""
	}
	c := n.NamedChild(start)
	if c == nil {
		return ""
	}
	switch c.Kind() {
	case "keyword_cascade":
		return "CASCADE"
	case "keyword_restrict":
		return "RESTRICT"
	case "keyword_set":
		if start+1 < n.NamedChildCount() {
			next := n.NamedChild(start + 1)
			if next != nil && next.Kind() == "keyword_null" {
				return "SET NULL"
			}
			if next != nil && next.Kind() == "keyword_default" {
				return "SET DEFAULT"
			}
		}
		return ""
	case "keyword_no":
		return "NO ACTION"
	}
	return strings.ToUpper(strings.TrimSpace(c.Utf8Text(source)))
}

// sqlExtractIndex extracts a CREATE [UNIQUE] INDEX statement.
func sqlExtractIndex(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, migTool string) {
	// Index name is the first direct identifier child (not in object_reference).
	var idxName string
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "identifier" {
			idxName = strings.TrimSpace(c.Utf8Text(source))
			break
		}
	}
	if idxName == "" {
		return
	}

	// Target table from object_reference.
	tableRef := sqlFindChild(n, "object_reference")
	targetTable := ""
	if tableRef != nil {
		targetTable = sqlQualifiedPath(tableRef, source)
	}

	// Unique: keyword_unique present.
	unique := false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c != nil && c.Kind() == "keyword_unique" {
			unique = true
			break
		}
	}

	// Indexed columns from index_fields.
	var idxCols []string
	if idxFields := sqlFindChild(n, "index_fields"); idxFields != nil {
		for i := uint(0); i < idxFields.NamedChildCount(); i++ {
			f := idxFields.NamedChild(i)
			if f == nil || f.Kind() != "field" {
				continue
			}
			id := sqlFindChild(f, "identifier")
			if id != nil {
				idxCols = append(idxCols, strings.TrimSpace(id.Utf8Text(source)))
			}
		}
	}

	// Schema defaults to public (index inherits the table's schema, but we
	// don't resolve it here — use public as the default for the index node).
	schema := "public"
	if tableRef != nil {
		s, _ := sqlObjectRef(tableRef, source)
		schema = s
	}
	idxPath := schema + "." + idxName

	meta := map[string]any{
		"schema": schema,
		"name":   idxName,
		"unique": unique,
	}
	if targetTable != "" {
		meta["table"] = targetTable
	}
	if len(idxCols) > 0 {
		meta["columns"] = idxCols
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "index",
		Title:    idxName,
		Path:     idxPath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})
}

// sqlExtractView extracts a CREATE [MATERIALIZED] VIEW statement.
func sqlExtractView(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, materialized bool, migTool string) {
	objRef := sqlFindChild(n, "object_reference")
	if objRef == nil {
		return
	}
	schema, viewName := sqlObjectRef(objRef, source)
	if viewName == "" {
		return
	}
	viewPath := schema + "." + viewName

	meta := map[string]any{
		"schema":       schema,
		"name":         viewName,
		"materialized": materialized,
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "view",
		Title:    viewName,
		Path:     viewPath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})
}

// sqlExtractFunction extracts a CREATE FUNCTION / CREATE PROCEDURE statement.
func sqlExtractFunction(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, migTool string) {
	objRef := sqlFindChild(n, "object_reference")
	if objRef == nil {
		return
	}
	schema, funcName := sqlObjectRef(objRef, source)
	if funcName == "" {
		return
	}

	// Extract argument types for overload disambiguation.
	var argTypes []string
	if argsNode := sqlFindChild(n, "function_arguments"); argsNode != nil {
		for i := uint(0); i < argsNode.NamedChildCount(); i++ {
			arg := argsNode.NamedChild(i)
			if arg == nil || arg.Kind() != "function_argument" {
				continue
			}
			argTypes = append(argTypes, sqlFunctionArgType(arg, source))
		}
	}

	// Return type: keyword_returns + following type node.
	returnType := sqlFunctionReturnType(n, source)

	// Language from function_language > identifier.
	language := ""
	if langNode := sqlFindChild(n, "function_language"); langNode != nil {
		id := sqlFindChild(langNode, "identifier")
		if id != nil {
			language = strings.TrimSpace(id.Utf8Text(source))
		}
	}

	// Path includes argument types for overload disambiguation.
	funcPath := schema + "." + funcName
	if len(argTypes) > 0 {
		funcPath += "(" + strings.Join(argTypes, ",") + ")"
	}

	meta := map[string]any{
		"schema": schema,
		"name":   funcName,
	}
	if returnType != "" {
		meta["returns"] = returnType
	}
	if language != "" {
		meta["language"] = language
	}
	if len(argTypes) > 0 {
		meta["args"] = argTypes
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	// For PL/pgSQL functions, extract body references (lib-o5dn.3).
	// plpgsqlExtractRefs needs the full CREATE FUNCTION statement text.
	// On parse failure, mark the function unit as partially parsed so
	// callers know the body_references set is incomplete.
	if strings.EqualFold(language, "plpgsql") {
		bodyRefs, ok := plpgsqlExtractRefs(funcPath, schema, n.Utf8Text(source))
		if !ok {
			meta["partial"] = true
		} else {
			doc.Refs = append(doc.Refs, bodyRefs...)
		}
	}

	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "function",
		Title:    funcName,
		Path:     funcPath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})
}

// sqlFunctionArgType extracts the type string from a function_argument node.
func sqlFunctionArgType(n *sitter.Node, source []byte) string {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil || c.Kind() == "identifier" {
			continue
		}
		// Type wrapper nodes (int, numeric, etc.): use their text trimmed.
		txt := strings.TrimSpace(c.Utf8Text(source))
		if txt != "" {
			return txt
		}
	}
	return ""
}

// sqlFunctionReturnType walks the create_function node for the return type
// following keyword_returns.
func sqlFunctionReturnType(n *sitter.Node, source []byte) string {
	seenReturns := false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "keyword_returns" {
			seenReturns = true
			continue
		}
		if seenReturns {
			if c.Kind() == "function_body" || c.Kind() == "function_language" {
				break
			}
			return strings.TrimSpace(c.Utf8Text(source))
		}
	}
	return ""
}

// sqlExtractSequence extracts a CREATE SEQUENCE statement.
func sqlExtractSequence(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, migTool string) {
	objRef := sqlFindChild(n, "object_reference")
	if objRef == nil {
		return
	}
	schema, seqName := sqlObjectRef(objRef, source)
	if seqName == "" {
		return
	}
	seqPath := schema + "." + seqName

	meta := map[string]any{
		"schema": schema,
		"name":   seqName,
	}
	// Extract start / increment values.
	seenStart, seenIncrement := false, false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Kind() {
		case "keyword_start":
			seenStart = true
		case "keyword_increment":
			seenIncrement = true
		case "literal":
			if seenStart && meta["start"] == nil {
				meta["start"] = strings.TrimSpace(c.Utf8Text(source))
				seenStart = false
			} else if seenIncrement && meta["increment"] == nil {
				meta["increment"] = strings.TrimSpace(c.Utf8Text(source))
				seenIncrement = false
			}
		}
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "sequence",
		Title:    seqName,
		Path:     seqPath,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})
}

// sqlExtractSchema extracts a CREATE SCHEMA statement.
func sqlExtractSchema(n *sitter.Node, source []byte, doc *indexer.ParsedDoc, migTool string) {
	// Schema name is the first direct identifier child.
	var schemaName string
	var authorization string
	seenAuth := false
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Kind() == "keyword_authorization" {
			seenAuth = true
			continue
		}
		if c.Kind() == "identifier" {
			if schemaName == "" {
				schemaName = strings.TrimSpace(c.Utf8Text(source))
			} else if seenAuth && authorization == "" {
				authorization = strings.TrimSpace(c.Utf8Text(source))
			}
		}
	}
	if schemaName == "" {
		return
	}

	meta := map[string]any{
		"name": schemaName,
	}
	if authorization != "" {
		meta["authorization"] = authorization
	}
	if migTool != "" {
		meta["migration_tool"] = migTool
	}

	doc.Units = append(doc.Units, indexer.Unit{
		Kind:     "schema",
		Title:    schemaName,
		Path:     schemaName,
		Content:  n.Utf8Text(source),
		Loc:      nodeLocation(n),
		Metadata: meta,
	})
}

// sqlExtractAlterTableFKs walks an alter_table node for ADD CONSTRAINT FOREIGN KEY.
func sqlExtractAlterTableFKs(n *sitter.Node, source []byte, doc *indexer.ParsedDoc) {
	objRef := sqlFindChild(n, "object_reference")
	if objRef == nil {
		return
	}
	tableSchema, tableName := sqlObjectRef(objRef, source)
	if tableName == "" {
		return
	}
	tablePath := tableSchema + "." + tableName

	addCon := sqlFindChild(n, "add_constraint")
	if addCon == nil {
		return
	}
	constraint := sqlFindChild(addCon, "constraint")
	if constraint == nil {
		return
	}
	sqlExtractConstraintFK(constraint, source, tablePath, doc)
}

// --- Migration tool detection ---

type migrationConvention struct {
	Tool          string
	DirNames      []string
	FileRegex     *regexp.Regexp
	ContentMarker string // if non-empty, file content must contain this substring
	SiblingFile   string // if non-empty, this file must exist in the same directory
}

// migrationConventions is an ordered allow-list matched by first-win semantics.
// dbmate, atlas, and goose all share DirNames and FileRegex, so they are
// disambiguated by additional constraints checked after the dir+regex match:
//   - dbmate:  content must contain "-- migrate:up"
//   - atlas:   directory must contain an "atlas.sum" sibling file
//   - goose:   fallback when neither dbmate nor atlas markers are present
var migrationConventions = []migrationConvention{
	{
		// dbmate: same dir+regex as goose, distinguished by content marker.
		Tool:          "dbmate",
		DirNames:      []string{"migrations", "db/migrations"},
		FileRegex:     regexp.MustCompile(`^\d{14}_.*\.sql$`),
		ContentMarker: "-- migrate:up",
	},
	{
		// atlas: same dir+regex as goose, distinguished by atlas.sum sibling.
		Tool:        "atlas",
		DirNames:    []string{"migrations", "db/migrations"},
		FileRegex:   regexp.MustCompile(`^\d{14}_.*\.sql$`),
		SiblingFile: "atlas.sum",
	},
	{
		// goose: fallback for 14-digit-prefix migrations with no dbmate/atlas markers.
		Tool:      "goose",
		DirNames:  []string{"migrations", "db/migrations"},
		FileRegex: regexp.MustCompile(`^\d{14}_.*\.sql$`),
	},
	{
		Tool:      "flyway",
		DirNames:  []string{"resources/db/migration", "db/migration"},
		FileRegex: regexp.MustCompile(`^V\d+__.*\.sql$`),
	},
	{
		// sqlx uses shorter numeric prefixes (e.g. 001_) that don't match the
		// 14-digit goose/dbmate/atlas pattern, so this entry is reachable.
		Tool:      "sqlx",
		DirNames:  []string{"migrations"},
		FileRegex: regexp.MustCompile(`^\d+_.*\.sql$`),
	},
}

// detectMigrationTool returns the first matching migration tool name for a
// .sql file, or "" if none match. Matching uses dir+regex as a first gate,
// then checks ContentMarker (substring in file content) and SiblingFile
// (sibling file existence) when present. First match wins.
func detectMigrationTool(path string, content []byte) string {
	dir := filepath.ToSlash(filepath.Dir(path))
	base := filepath.Base(path)

	for _, conv := range migrationConventions {
		dirMatches := false
		for _, pattern := range conv.DirNames {
			if dir == pattern || strings.HasSuffix(dir, "/"+pattern) {
				dirMatches = true
				break
			}
		}
		if !dirMatches || !conv.FileRegex.MatchString(base) {
			continue
		}
		if conv.ContentMarker != "" && !bytes.Contains(content, []byte(conv.ContentMarker)) {
			continue
		}
		if conv.SiblingFile != "" && !siblingExists(path, conv.SiblingFile) {
			continue
		}
		return conv.Tool
	}
	return ""
}

// siblingExists reports whether name exists as a file sibling to path.
func siblingExists(path, name string) bool {
	_, err := os.Stat(filepath.Join(filepath.Dir(path), name))
	return err == nil
}

func init() {
	// RegisterDefaultAdditional — the v1 sql handler (internal/indexer/handlers/sql)
	// remains the primary handler for .sql/.psql/.ddl (docs-pass chunking). This
	// grammar adds graph-pass symbol extraction as an additional handler. The
	// graph-pass walker merges Units/Refs from all handlers via HandlersFor.
	indexer.RegisterDefaultAdditional(New(NewSQLGrammar()))
}
