package python

import (
	"context"
	json "encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/sqlc-dev/plugin-sdk-go/metadata"
	"github.com/sqlc-dev/plugin-sdk-go/plugin"
	"github.com/sqlc-dev/plugin-sdk-go/sdk"

	pyast "github.com/sqlc-dev/sqlc-gen-python/internal/ast"
	"github.com/sqlc-dev/sqlc-gen-python/internal/inflection"
	"github.com/sqlc-dev/sqlc-gen-python/internal/poet"
	pyprint "github.com/sqlc-dev/sqlc-gen-python/internal/printer"
)

type Constant struct {
	Name  string
	Type  string
	Value string
}

type Enum struct {
	Name      string
	Comment   string
	Constants []Constant
}

type pyType struct {
	InnerType string
	IsArray   bool
	IsNull    bool
}

func (t pyType) Annotation() *pyast.Node {
	ann := poet.Name(t.InnerType)
	if t.IsArray {
		ann = subscriptNode("List", ann)
	}
	if t.IsNull {
		ann = subscriptNode("Optional", ann)
	}
	return ann
}

type Field struct {
	Name    string
	Type    pyType
	Comment string
}

type Struct struct {
	Table   plugin.Identifier
	Name    string
	Fields  []Field
	Comment string
}

type QueryValue struct {
	Emit   bool
	Name   string
	Struct *Struct
	Typ    pyType
}

func (v QueryValue) Annotation() *pyast.Node {
	if v.Typ != (pyType{}) {
		return v.Typ.Annotation()
	}
	if v.Struct != nil {
		if v.Emit {
			return poet.Name(v.Struct.Name)
		} else {
			return typeRefNode("models", v.Struct.Name)
		}
	}
	panic("no type for QueryValue: " + v.Name)
}

func (v QueryValue) EmitStruct() bool {
	return v.Emit
}

func (v QueryValue) IsStruct() bool {
	return v.Struct != nil
}

func (v QueryValue) isEmpty() bool {
	return v.Typ == (pyType{}) && v.Name == "" && v.Struct == nil
}

func (v QueryValue) RowNode(rowVar string) *pyast.Node {
	if !v.IsStruct() {
		return subscriptNode(
			rowVar,
			constantInt(0),
		)
	}
	call := &pyast.Call{
		Func: v.Annotation(),
	}
	for i, f := range v.Struct.Fields {
		call.Keywords = append(call.Keywords, &pyast.Keyword{
			Arg: f.Name,
			Value: subscriptNode(
				rowVar,
				constantInt(i),
			),
		})
	}
	return &pyast.Node{
		Node: &pyast.Node_Call{
			Call: call,
		},
	}
}

// A struct used to generate methods and fields on the Queries struct
type Query struct {
	Cmd            string
	Comments       []string
	MethodName     string
	FieldName      string
	ConstantName   string
	SQL            string
	SourceName     string
	Ret            QueryValue
	Args           []QueryValue
	PositionalArgs []string
}

func (q Query) AddArgs(args *pyast.Arguments) {
	// A single struct arg does not need to be passed as a keyword argument
	if len(q.Args) == 1 && q.Args[0].IsStruct() {
		args.Args = append(args.Args, &pyast.Arg{
			Arg:        q.Args[0].Name,
			Annotation: q.Args[0].Annotation(),
		})
		return
	}
	for _, a := range q.Args {
		args.KwOnlyArgs = append(args.KwOnlyArgs, &pyast.Arg{
			Arg:        a.Name,
			Annotation: a.Annotation(),
		})
	}
}

func (q Query) ArgDictNode() *pyast.Node {
	dict := &pyast.Dict{}
	i := 1
	for _, a := range q.Args {
		if a.isEmpty() {
			continue
		}
		if a.IsStruct() {
			for _, f := range a.Struct.Fields {
				dict.Keys = append(dict.Keys, poet.Constant(fmt.Sprintf("p%v", i)))
				dict.Values = append(dict.Values, typeRefNode(a.Name, f.Name))
				i++
			}
		} else {
			dict.Keys = append(dict.Keys, poet.Constant(fmt.Sprintf("p%v", i)))
			dict.Values = append(dict.Values, poet.Name(a.Name))
			i++
		}
	}
	if len(dict.Keys) == 0 {
		return nil
	}
	return &pyast.Node{
		Node: &pyast.Node_Dict{
			Dict: dict,
		},
	}
}

func (q Query) PositionalTupleNode() *pyast.Node {
	switch len(q.PositionalArgs) {
	case 0:
		return poet.Name("()")
	case 1:
		return poet.Name("(" + q.PositionalArgs[0] + ",)")
	default:
		return poet.Name("(" + strings.Join(q.PositionalArgs, ", ") + ")")
	}
}

func makePyType(req *plugin.GenerateRequest, col *plugin.Column) pyType {
	typ := pyInnerType(req, col)
	return pyType{
		InnerType: typ,
		IsArray:   col.IsArray,
		IsNull:    !col.NotNull,
	}
}

func pyInnerType(req *plugin.GenerateRequest, col *plugin.Column) string {
	switch req.Settings.Engine {
	case "postgresql":
		return postgresType(req, col)
	case "sqlite":
		return sqliteType(col)
	default:
		log.Println("unsupported engine type")
		return "Any"
	}
}

func modelName(name string, settings *plugin.Settings) string {
	out := ""
	for _, p := range strings.Split(name, "_") {
		out += strings.Title(p)
	}
	return out
}

var matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCap = regexp.MustCompile("([a-z0-9])([A-Z])")

func methodName(name string) string {
	snake := matchFirstCap.ReplaceAllString(name, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}

var pyIdentPattern = regexp.MustCompile("[^a-zA-Z0-9_]+")

func pyEnumValueName(value string) string {
	id := strings.Replace(value, "-", "_", -1)
	id = strings.Replace(id, ":", "_", -1)
	id = strings.Replace(id, "/", "_", -1)
	id = pyIdentPattern.ReplaceAllString(id, "")
	return strings.ToUpper(id)
}

func buildEnums(req *plugin.GenerateRequest) []Enum {
	var enums []Enum
	for _, schema := range req.Catalog.Schemas {
		if schema.Name == "pg_catalog" || schema.Name == "information_schema" {
			continue
		}
		for _, enum := range schema.Enums {
			var enumName string
			if schema.Name == req.Catalog.DefaultSchema {
				enumName = enum.Name
			} else {
				enumName = schema.Name + "_" + enum.Name
			}
			e := Enum{
				Name:    modelName(enumName, req.Settings),
				Comment: enum.Comment,
			}
			for _, v := range enum.Vals {
				e.Constants = append(e.Constants, Constant{
					Name:  pyEnumValueName(v),
					Value: v,
					Type:  e.Name,
				})
			}
			enums = append(enums, e)
		}
	}
	if len(enums) > 0 {
		sort.Slice(enums, func(i, j int) bool { return enums[i].Name < enums[j].Name })
	}
	return enums
}

func buildModels(conf Config, req *plugin.GenerateRequest) []Struct {
	var structs []Struct
	for _, schema := range req.Catalog.Schemas {
		if schema.Name == "pg_catalog" || schema.Name == "information_schema" {
			continue
		}
		for _, table := range schema.Tables {
			var tableName string
			if schema.Name == req.Catalog.DefaultSchema {
				tableName = table.Rel.Name
			} else {
				tableName = schema.Name + "_" + table.Rel.Name
			}
			structName := tableName
			if !conf.EmitExactTableNames {
				structName = inflection.Singular(inflection.SingularParams{
					Name:       structName,
					Exclusions: conf.InflectionExcludeTableNames,
				})
			}
			s := Struct{
				Table:   plugin.Identifier{Schema: schema.Name, Name: table.Rel.Name},
				Name:    modelName(structName, req.Settings),
				Comment: table.Comment,
			}
			for _, column := range table.Columns {
				typ := makePyType(req, column) // TODO: This used to call compiler.ConvertColumn?
				typ.InnerType = strings.TrimPrefix(typ.InnerType, "models.")
				s.Fields = append(s.Fields, Field{
					Name:    column.Name,
					Type:    typ,
					Comment: column.Comment,
				})
			}
			structs = append(structs, s)
		}
	}
	if len(structs) > 0 {
		sort.Slice(structs, func(i, j int) bool { return structs[i].Name < structs[j].Name })
	}
	return structs
}

func columnName(c *plugin.Column, pos int) string {
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("column_%d", pos+1)
}

func paramName(p *plugin.Parameter) string {
	if p.Column.Name != "" {
		return p.Column.Name
	}
	return fmt.Sprintf("dollar_%d", p.Number)
}

type pyColumn struct {
	id int32
	*plugin.Column
}

func columnsToStruct(req *plugin.GenerateRequest, name string, columns []pyColumn) *Struct {
	gs := Struct{
		Name: name,
	}
	seen := map[string]int32{}
	suffixes := map[int32]int32{}
	for i, c := range columns {
		colName := columnName(c.Column, i)
		fieldName := colName
		// Track suffixes by the ID of the column, so that columns referring to
		// the same numbered parameter can be reused.
		var suffix int32
		if o, ok := suffixes[c.id]; ok {
			suffix = o
		} else if v := seen[colName]; v > 0 {
			suffix = v + 1
		}
		suffixes[c.id] = suffix
		if suffix > 0 {
			fieldName = fmt.Sprintf("%s_%d", fieldName, suffix)
		}
		gs.Fields = append(gs.Fields, Field{
			Name: fieldName,
			Type: makePyType(req, c.Column),
		})
		seen[colName]++
	}
	return &gs
}

var postgresPlaceholderRegexp = regexp.MustCompile(`\B\$(\d+)\b`)

// Sqlalchemy uses ":name" for placeholders, so "$N" is converted to ":pN"
// This also means ":" has special meaning to sqlalchemy, so it must be escaped.
func sqlalchemySQL(s, engine string) string {
	s = strings.ReplaceAll(s, ":", `\\:`)
	if engine == "postgresql" {
		return postgresPlaceholderRegexp.ReplaceAllString(s, ":p$1")
	}
	return s
}

// dbapiSQL converts SQLite's numbered placeholders to the qmark paramstyle and
// returns their logical parameter numbers in occurrence order. Keeping this
// order separate from query.Params is important when the same logical
// parameter occurs more than once in a statement.
func dbapiSQL(s, engine string) (string, []int32, error) {
	if engine != "sqlite" {
		return "", nil, fmt.Errorf("driver %q only supports the sqlite engine", driverDBAPI)
	}

	next := int32(1)
	var order []int32
	var sql strings.Builder
	sql.Grow(len(s))
	for index := 0; index < len(s); {
		switch {
		case s[index] == '\'' || s[index] == '"' || s[index] == '`':
			quote := s[index]
			sql.WriteByte(quote)
			index++
			for index < len(s) {
				sql.WriteByte(s[index])
				if s[index] != quote {
					index++
					continue
				}
				index++
				if index < len(s) && s[index] == quote {
					sql.WriteByte(s[index])
					index++
					continue
				}
				break
			}
		case s[index] == '[':
			end := strings.IndexByte(s[index+1:], ']')
			if end < 0 {
				sql.WriteString(s[index:])
				index = len(s)
			} else {
				end += index + 2
				sql.WriteString(s[index:end])
				index = end
			}
		case strings.HasPrefix(s[index:], "--"):
			end := strings.IndexByte(s[index:], '\n')
			if end < 0 {
				sql.WriteString(s[index:])
				index = len(s)
			} else {
				end += index + 1
				sql.WriteString(s[index:end])
				index = end
			}
		case strings.HasPrefix(s[index:], "/*"):
			end := strings.Index(s[index+2:], "*/")
			if end < 0 {
				sql.WriteString(s[index:])
				index = len(s)
			} else {
				end += index + 4
				sql.WriteString(s[index:end])
				index = end
			}
		case s[index] == '?':
			end := index + 1
			for end < len(s) && s[end] >= '0' && s[end] <= '9' {
				end++
			}
			placeholder := s[index:end]
			number := next
			if len(placeholder) > 1 {
				parsed, err := strconv.ParseInt(placeholder[1:], 10, 32)
				if err != nil || parsed < 1 {
					return "", nil, fmt.Errorf("invalid SQLite placeholder %q", placeholder)
				}
				number = int32(parsed)
				if number >= next {
					next = number + 1
				}
			} else {
				next++
			}
			order = append(order, number)
			sql.WriteByte('?')
			index = end
		default:
			sql.WriteByte(s[index])
			index++
		}
	}
	return sql.String(), order, nil
}

func uniqueQueryParams(params []*plugin.Parameter) []*plugin.Parameter {
	seen := make(map[int32]struct{}, len(params))
	unique := make([]*plugin.Parameter, 0, len(params))
	for _, param := range params {
		if _, ok := seen[param.Number]; ok {
			continue
		}
		seen[param.Number] = struct{}{}
		unique = append(unique, param)
	}
	return unique
}

func buildQueries(conf Config, req *plugin.GenerateRequest, structs []Struct) ([]Query, error) {
	qs := make([]Query, 0, len(req.Queries))
	for _, query := range req.Queries {
		if query.Name == "" {
			continue
		}
		if query.Cmd == "" {
			continue
		}
		if query.Cmd == metadata.CmdCopyFrom {
			return nil, errors.New("Support for CopyFrom in Python is not implemented")
		}

		methodName := methodName(query.Name)

		querySQL := sqlalchemySQL(query.Text, req.Settings.Engine)
		var positionalOrder []int32
		if conf.driver() == driverDBAPI {
			var err error
			querySQL, positionalOrder, err = dbapiSQL(query.Text, req.Settings.Engine)
			if err != nil {
				return nil, err
			}
		}

		gq := Query{
			Cmd:          query.Cmd,
			Comments:     query.Comments,
			MethodName:   methodName,
			FieldName:    sdk.LowerTitle(query.Name) + "Stmt",
			ConstantName: strings.ToUpper(methodName),
			SQL:          querySQL,
			SourceName:   query.Filename,
		}

		qpl := 4
		if conf.QueryParameterLimit != nil {
			qpl = int(*conf.QueryParameterLimit)
		}
		if qpl < 0 {
			return nil, errors.New("invalid query parameter limit")
		}
		params := query.Params
		if conf.driver() == driverDBAPI {
			params = uniqueQueryParams(params)
		}
		paramExpressions := make(map[int32]string, len(params))
		if len(params) > qpl || qpl == 0 {
			var cols []pyColumn
			for _, p := range params {
				cols = append(cols, pyColumn{
					id:     p.Number,
					Column: p.Column,
				})
			}
			gq.Args = []QueryValue{{
				Emit:   true,
				Name:   "arg",
				Struct: columnsToStruct(req, query.Name+"Params", cols),
			}}
			for index, p := range params {
				paramExpressions[p.Number] = "arg." + gq.Args[0].Struct.Fields[index].Name
			}
		} else {
			args := make([]QueryValue, 0, len(params))
			for _, p := range params {
				args = append(args, QueryValue{
					Name: paramName(p),
					Typ:  makePyType(req, p.Column),
				})
				paramExpressions[p.Number] = paramName(p)
			}
			gq.Args = args
		}
		for _, number := range positionalOrder {
			expression, ok := paramExpressions[number]
			if !ok {
				return nil, fmt.Errorf("SQLite placeholder ?%d has no matching query parameter in %s", number, query.Name)
			}
			gq.PositionalArgs = append(gq.PositionalArgs, expression)
		}

		if len(query.Columns) == 1 {
			c := query.Columns[0]
			gq.Ret = QueryValue{
				Name: columnName(c, 0),
				Typ:  makePyType(req, c),
			}
		} else if len(query.Columns) > 1 {
			var gs *Struct
			var emit bool

			for _, s := range structs {
				if len(s.Fields) != len(query.Columns) {
					continue
				}
				same := true

				for i, f := range s.Fields {
					c := query.Columns[i]
					// HACK: models do not have "models." on their types, so trim that so we can find matches
					trimmedPyType := makePyType(req, c)
					trimmedPyType.InnerType = strings.TrimPrefix(trimmedPyType.InnerType, "models.")
					sameName := f.Name == columnName(c, i)
					sameType := f.Type == trimmedPyType
					sameTable := sdk.SameTableName(c.Table, &s.Table, req.Catalog.DefaultSchema)
					if !sameName || !sameType || !sameTable {
						same = false
					}
				}
				if same {
					gs = &s
					break
				}
			}

			if gs == nil {
				var columns []pyColumn
				for i, c := range query.Columns {
					columns = append(columns, pyColumn{
						id:     int32(i),
						Column: c,
					})
				}
				gs = columnsToStruct(req, query.Name+"Row", columns)
				emit = true
			}
			gq.Ret = QueryValue{
				Emit:   emit,
				Name:   "i",
				Struct: gs,
			}
		}

		qs = append(qs, gq)
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].MethodName < qs[j].MethodName })
	return qs, nil
}

func moduleNode(version, source string) *pyast.Module {
	mod := &pyast.Module{
		Body: []*pyast.Node{
			poet.Comment(
				"Code generated by sqlc. DO NOT EDIT.",
			),
			poet.Comment(
				"versions:",
			),
			poet.Comment(
				"  sqlc " + version,
			),
		},
	}
	if source != "" {
		mod.Body = append(mod.Body,
			poet.Comment(
				"source: "+source,
			),
		)
	}
	return mod
}

func importNode(name string) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_Import{
			Import: &pyast.Import{
				Names: []*pyast.Node{
					{
						Node: &pyast.Node_Alias{
							Alias: &pyast.Alias{
								Name: name,
							},
						},
					},
				},
			},
		},
	}
}

func classDefNode(name string, bases ...*pyast.Node) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_ClassDef{
			ClassDef: &pyast.ClassDef{
				Name:  name,
				Bases: bases,
			},
		},
	}
}

func assignNode(target string, value *pyast.Node) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_Assign{
			Assign: &pyast.Assign{
				Targets: []*pyast.Node{
					poet.Name(target),
				},
				Value: value,
			},
		},
	}
}

func constantInt(value int) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_Constant{
			Constant: &pyast.Constant{
				Value: &pyast.Constant_Int{
					Int: int32(value),
				},
			},
		},
	}
}

func subscriptNode(value string, slice *pyast.Node) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_Subscript{
			Subscript: &pyast.Subscript{
				Value: &pyast.Name{Id: value},
				Slice: slice,
			},
		},
	}
}

func dataclassNode(name string) *pyast.ClassDef {
	return &pyast.ClassDef{
		Name: name,
		DecoratorList: []*pyast.Node{
			{
				Node: &pyast.Node_Call{
					Call: &pyast.Call{
						Func: poet.Attribute(poet.Name("dataclasses"), "dataclass"),
					},
				},
			},
		},
	}
}

func pydanticNode(name string) *pyast.ClassDef {
	return &pyast.ClassDef{
		Name: name,
		Bases: []*pyast.Node{
			{
				Node: &pyast.Node_Attribute{
					Attribute: &pyast.Attribute{
						Value: &pyast.Node{
							Node: &pyast.Node_Name{
								Name: &pyast.Name{Id: "pydantic"},
							},
						},
						Attr: "BaseModel",
					},
				},
			},
		},
	}
}

func fieldNode(f Field) *pyast.Node {
	return &pyast.Node{
		Node: &pyast.Node_AnnAssign{
			AnnAssign: &pyast.AnnAssign{
				Target:     &pyast.Name{Id: f.Name},
				Annotation: f.Type.Annotation(),
				Comment:    f.Comment,
			},
		},
	}
}

func typeRefNode(base string, parts ...string) *pyast.Node {
	n := poet.Name(base)
	for _, p := range parts {
		n = poet.Attribute(n, p)
	}
	return n
}

func connMethodNode(driver, method, name string, arg *pyast.Node) *pyast.Node {
	var statement *pyast.Node
	if driver == driverDBAPI {
		statement = poet.Name(name)
	} else {
		statement = &pyast.Node{
			Node: &pyast.Node_Call{
				Call: &pyast.Call{
					Func: typeRefNode("sqlalchemy", "text"),
					Args: []*pyast.Node{
						poet.Name(name),
					},
				},
			},
		}
	}
	args := []*pyast.Node{statement}
	if arg != nil {
		args = append(args, arg)
	}
	return &pyast.Node{
		Node: &pyast.Node_Call{
			Call: &pyast.Call{
				Func: typeRefNode("self", "_conn", method),
				Args: args,
			},
		},
	}
}

func execRowsBody(driver string, q Query, exec *pyast.Node, async bool) []*pyast.Node {
	result := exec
	if async {
		result = poet.Await(exec)
	}
	body := []*pyast.Node{assignNode("result", result)}
	if driver == driverDBAPI && !q.Ret.isEmpty() {
		// SQLite does not finalize a DML statement with RETURNING until all
		// returned rows are consumed. Some DB-API implementations only update
		// rowcount at that point.
		body = append(body, poet.Expr(poet.Node(&pyast.Call{
			Func: typeRefNode("result", "fetchall"),
		})))
	}
	return append(body, poet.Return(poet.Attribute(poet.Name("result"), "rowcount")))
}

func buildImportGroup(specs map[string]importSpec) *pyast.Node {
	var body []*pyast.Node
	for _, spec := range buildImportBlock2(specs) {
		if len(spec.Names) > 0 && spec.Names[0] != "" {
			imp := &pyast.ImportFrom{
				Module: spec.Module,
			}
			for _, name := range spec.Names {
				imp.Names = append(imp.Names, poet.Alias(name))
			}
			body = append(body, &pyast.Node{
				Node: &pyast.Node_ImportFrom{
					ImportFrom: imp,
				},
			})
		} else {
			body = append(body, importNode(spec.Module))
		}
	}
	return &pyast.Node{
		Node: &pyast.Node_ImportGroup{
			ImportGroup: &pyast.ImportGroup{
				Imports: body,
			},
		},
	}
}

func buildModelsTree(ctx *pyTmplCtx, i *importer) *pyast.Node {
	mod := moduleNode(ctx.SqlcVersion, "")
	std, pkg := i.modelImportSpecs()
	mod.Body = append(mod.Body, buildImportGroup(std), buildImportGroup(pkg))

	for _, e := range ctx.Enums {
		bases := []*pyast.Node{
			poet.Name("str"),
			poet.Attribute(poet.Name("enum"), "Enum"),
		}
		if i.C.EmitStrEnum {
			// override the bases to emit enum.StrEnum (only support in Python >=3.11)
			bases = []*pyast.Node{
				poet.Attribute(poet.Name("enum"), "StrEnum"),
			}
		}
		def := &pyast.ClassDef{
			Name:  e.Name,
			Bases: bases,
		}
		if e.Comment != "" {
			def.Body = append(def.Body, &pyast.Node{
				Node: &pyast.Node_Expr{
					Expr: &pyast.Expr{
						Value: poet.Constant(e.Comment),
					},
				},
			})
		}
		for _, c := range e.Constants {
			def.Body = append(def.Body, assignNode(c.Name, poet.Constant(c.Value)))
		}
		mod.Body = append(mod.Body, &pyast.Node{
			Node: &pyast.Node_ClassDef{
				ClassDef: def,
			},
		})
	}

	for _, m := range ctx.Models {
		var def *pyast.ClassDef
		if ctx.C.EmitPydanticModels {
			def = pydanticNode(m.Name)
		} else {
			def = dataclassNode(m.Name)
		}
		if m.Comment != "" {
			def.Body = append(def.Body, &pyast.Node{
				Node: &pyast.Node_Expr{
					Expr: &pyast.Expr{
						Value: poet.Constant(m.Comment),
					},
				},
			})
		}
		for _, f := range m.Fields {
			def.Body = append(def.Body, fieldNode(f))
		}
		mod.Body = append(mod.Body, &pyast.Node{
			Node: &pyast.Node_ClassDef{
				ClassDef: def,
			},
		})
	}

	return &pyast.Node{Node: &pyast.Node_Module{Module: mod}}
}

func querierClassDef(driver string) *pyast.ClassDef {
	connectionType := typeRefNode("sqlalchemy", "engine", "Connection")
	if driver == driverDBAPI {
		connectionType = poet.Name("_DBAPIConnection")
	}
	return &pyast.ClassDef{
		Name: "Querier",
		Body: []*pyast.Node{
			{
				Node: &pyast.Node_FunctionDef{
					FunctionDef: &pyast.FunctionDef{
						Name: "__init__",
						Args: &pyast.Arguments{
							Args: []*pyast.Arg{
								{
									Arg: "self",
								},
								{
									Arg:        "conn",
									Annotation: connectionType,
								},
							},
						},
						Body: []*pyast.Node{
							{
								Node: &pyast.Node_Assign{
									Assign: &pyast.Assign{
										Targets: []*pyast.Node{
											poet.Attribute(poet.Name("self"), "_conn"),
										},
										Value: poet.Name("conn"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func asyncQuerierClassDef(driver string) *pyast.ClassDef {
	connectionType := typeRefNode("sqlalchemy", "ext", "asyncio", "AsyncConnection")
	if driver == driverDBAPI {
		connectionType = poet.Name("_AsyncDBAPIConnection")
	}
	return &pyast.ClassDef{
		Name: "AsyncQuerier",
		Body: []*pyast.Node{
			{
				Node: &pyast.Node_FunctionDef{
					FunctionDef: &pyast.FunctionDef{
						Name: "__init__",
						Args: &pyast.Arguments{
							Args: []*pyast.Arg{
								{
									Arg: "self",
								},
								{
									Arg:        "conn",
									Annotation: connectionType,
								},
							},
						},
						Body: []*pyast.Node{
							{
								Node: &pyast.Node_Assign{
									Assign: &pyast.Assign{
										Targets: []*pyast.Node{
											poet.Attribute(poet.Name("self"), "_conn"),
										},
										Value: poet.Name("conn"),
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func dbapiProtocolNodes(conf Config) []*pyast.Node {
	pass := func() *pyast.Node {
		return &pyast.Node{Node: &pyast.Node_Pass{Pass: &pyast.Pass{}}}
	}
	cursor := &pyast.ClassDef{
		Name:  "_DBAPICursor",
		Bases: []*pyast.Node{poet.Name("Protocol")},
		Body: []*pyast.Node{
			poet.Node(&pyast.FunctionDef{
				Name:          "rowcount",
				Args:          &pyast.Arguments{Args: []*pyast.Arg{{Arg: "self"}}},
				Body:          []*pyast.Node{pass()},
				Returns:       poet.Name("int"),
				DecoratorList: []*pyast.Node{poet.Name("property")},
			}),
			poet.Node(&pyast.FunctionDef{
				Name:    "fetchone",
				Args:    &pyast.Arguments{Args: []*pyast.Arg{{Arg: "self"}}},
				Body:    []*pyast.Node{pass()},
				Returns: poet.Name("Optional[Sequence[Any]]"),
			}),
			poet.Node(&pyast.FunctionDef{
				Name:    "fetchall",
				Args:    &pyast.Arguments{Args: []*pyast.Arg{{Arg: "self"}}},
				Body:    []*pyast.Node{pass()},
				Returns: poet.Name("Sequence[Sequence[Any]]"),
			}),
		},
	}
	nodes := []*pyast.Node{poet.Node(cursor)}
	executeArgs := func() *pyast.Arguments {
		return &pyast.Arguments{PosOnlyArgs: []*pyast.Arg{
			{Arg: "self"},
			{Arg: "sql", Annotation: poet.Name("str")},
			{Arg: "parameters", Annotation: poet.Name("tuple[Any, ...]")},
		}}
	}
	if conf.EmitSyncQuerier {
		nodes = append(nodes, poet.Node(&pyast.ClassDef{
			Name:  "_DBAPIConnection",
			Bases: []*pyast.Node{poet.Name("Protocol")},
			Body: []*pyast.Node{poet.Node(&pyast.FunctionDef{
				Name:    "execute",
				Args:    executeArgs(),
				Body:    []*pyast.Node{pass()},
				Returns: poet.Name("_DBAPICursor"),
			})},
		}))
	}
	if conf.EmitAsyncQuerier {
		nodes = append(nodes, poet.Node(&pyast.ClassDef{
			Name:  "_AsyncDBAPIConnection",
			Bases: []*pyast.Node{poet.Name("Protocol")},
			Body: []*pyast.Node{poet.Node(&pyast.AsyncFunctionDef{
				Name:    "execute",
				Args:    executeArgs(),
				Body:    []*pyast.Node{pass()},
				Returns: poet.Name("_DBAPICursor"),
			})},
		}))
	}
	return nodes
}

func buildQueryTree(ctx *pyTmplCtx, i *importer, source string) *pyast.Node {
	mod := moduleNode(ctx.SqlcVersion, source)
	std, pkg := i.queryImportSpecs(source)
	mod.Body = append(mod.Body, buildImportGroup(std), buildImportGroup(pkg))
	mod.Body = append(mod.Body, &pyast.Node{
		Node: &pyast.Node_ImportGroup{
			ImportGroup: &pyast.ImportGroup{
				Imports: []*pyast.Node{
					{
						Node: &pyast.Node_ImportFrom{
							ImportFrom: &pyast.ImportFrom{
								Module: ctx.C.Package,
								Names: []*pyast.Node{
									poet.Alias("models"),
								},
							},
						},
					},
				},
			},
		},
	})
	if ctx.C.driver() == driverDBAPI && (ctx.C.EmitSyncQuerier || ctx.C.EmitAsyncQuerier) {
		mod.Body = append(mod.Body, dbapiProtocolNodes(ctx.C)...)
	}

	for _, q := range ctx.Queries {
		if !ctx.OutputQuery(q.SourceName) {
			continue
		}
		queryText := fmt.Sprintf("-- name: %s \\\\%s\n%s\n", q.MethodName, q.Cmd, q.SQL)
		mod.Body = append(mod.Body, assignNode(q.ConstantName, poet.Constant(queryText)))
		for _, arg := range q.Args {
			if arg.EmitStruct() {
				var def *pyast.ClassDef
				if ctx.C.EmitPydanticModels {
					def = pydanticNode(arg.Struct.Name)
				} else {
					def = dataclassNode(arg.Struct.Name)
				}
				for _, f := range arg.Struct.Fields {
					def.Body = append(def.Body, fieldNode(f))
				}
				mod.Body = append(mod.Body, poet.Node(def))
			}
		}
		if q.Ret.EmitStruct() {
			var def *pyast.ClassDef
			if ctx.C.EmitPydanticModels {
				def = pydanticNode(q.Ret.Struct.Name)
			} else {
				def = dataclassNode(q.Ret.Struct.Name)
			}
			for _, f := range q.Ret.Struct.Fields {
				def.Body = append(def.Body, fieldNode(f))
			}
			mod.Body = append(mod.Body, poet.Node(def))
		}
	}

	if ctx.C.EmitSyncQuerier {
		driver := ctx.C.driver()
		cls := querierClassDef(driver)
		for _, q := range ctx.Queries {
			if !ctx.OutputQuery(q.SourceName) {
				continue
			}
			f := &pyast.FunctionDef{
				Name: q.MethodName,
				Args: &pyast.Arguments{
					Args: []*pyast.Arg{
						{
							Arg: "self",
						},
					},
				},
			}

			q.AddArgs(f.Args)
			execArg := q.ArgDictNode()
			if driver == driverDBAPI {
				execArg = q.PositionalTupleNode()
			}
			exec := connMethodNode(driver, "execute", q.ConstantName, execArg)

			switch q.Cmd {
			case ":one":
				fetchOne := poet.Node(&pyast.Call{Func: poet.Attribute(exec, "first")})
				if driver == driverDBAPI {
					f.Body = append(f.Body, assignNode("cursor", exec))
					fetchOne = poet.Node(&pyast.Call{Func: typeRefNode("cursor", "fetchone")})
				}
				f.Body = append(f.Body,
					assignNode("row", fetchOne),
					poet.Node(
						&pyast.If{
							Test: poet.Node(
								&pyast.Compare{
									Left: poet.Name("row"),
									Ops: []*pyast.Node{
										poet.Is(),
									},
									Comparators: []*pyast.Node{
										poet.Constant(nil),
									},
								},
							),
							Body: []*pyast.Node{
								poet.Return(
									poet.Constant(nil),
								),
							},
						},
					),
					poet.Return(q.Ret.RowNode("row")),
				)
				f.Returns = subscriptNode("Optional", q.Ret.Annotation())
			case ":many":
				result := exec
				if driver == driverDBAPI {
					f.Body = append(f.Body, assignNode("cursor", exec))
					result = poet.Node(&pyast.Call{Func: typeRefNode("cursor", "fetchall")})
				}
				f.Body = append(f.Body,
					assignNode("result", result),
					poet.Node(
						&pyast.For{
							Target: poet.Name("row"),
							Iter:   poet.Name("result"),
							Body: []*pyast.Node{
								poet.Expr(
									poet.Yield(
										q.Ret.RowNode("row"),
									),
								),
							},
						},
					),
				)
				f.Returns = subscriptNode("Iterator", q.Ret.Annotation())
			case ":exec":
				f.Body = append(f.Body, exec)
				f.Returns = poet.Constant(nil)
			case ":execrows":
				f.Body = append(f.Body, execRowsBody(driver, q, exec, false)...)
				f.Returns = poet.Name("int")
			case ":execresult":
				f.Body = append(f.Body,
					poet.Return(exec),
				)
				if driver == driverDBAPI {
					f.Returns = poet.Name("_DBAPICursor")
				} else {
					f.Returns = typeRefNode("sqlalchemy", "engine", "Result")
				}
			default:
				panic("unknown cmd " + q.Cmd)
			}

			cls.Body = append(cls.Body, poet.Node(f))
		}
		mod.Body = append(mod.Body, poet.Node(cls))
	}

	if ctx.C.EmitAsyncQuerier {
		driver := ctx.C.driver()
		cls := asyncQuerierClassDef(driver)
		for _, q := range ctx.Queries {
			if !ctx.OutputQuery(q.SourceName) {
				continue
			}
			f := &pyast.AsyncFunctionDef{
				Name: q.MethodName,
				Args: &pyast.Arguments{
					Args: []*pyast.Arg{
						{
							Arg: "self",
						},
					},
				},
			}

			q.AddArgs(f.Args)
			execArg := q.ArgDictNode()
			if driver == driverDBAPI {
				execArg = q.PositionalTupleNode()
			}
			exec := connMethodNode(driver, "execute", q.ConstantName, execArg)

			switch q.Cmd {
			case ":one":
				fetchOne := poet.Node(&pyast.Call{
					Func: poet.Attribute(poet.Await(exec), "first"),
				})
				if driver == driverDBAPI {
					f.Body = append(f.Body, assignNode("cursor", poet.Await(exec)))
					fetchOne = poet.Node(&pyast.Call{Func: typeRefNode("cursor", "fetchone")})
				}
				f.Body = append(f.Body,
					assignNode("row", fetchOne),
					poet.Node(
						&pyast.If{
							Test: poet.Node(
								&pyast.Compare{
									Left: poet.Name("row"),
									Ops: []*pyast.Node{
										poet.Is(),
									},
									Comparators: []*pyast.Node{
										poet.Constant(nil),
									},
								},
							),
							Body: []*pyast.Node{
								poet.Return(
									poet.Constant(nil),
								),
							},
						},
					),
					poet.Return(q.Ret.RowNode("row")),
				)
				f.Returns = subscriptNode("Optional", q.Ret.Annotation())
			case ":many":
				if driver == driverDBAPI {
					f.Body = append(f.Body,
						assignNode("cursor", poet.Await(exec)),
						assignNode("result", poet.Node(&pyast.Call{Func: typeRefNode("cursor", "fetchall")})),
						poet.Node(&pyast.For{
							Target: poet.Name("row"),
							Iter:   poet.Name("result"),
							Body: []*pyast.Node{
								poet.Expr(
									poet.Yield(
										q.Ret.RowNode("row"),
									),
								),
							},
						}),
					)
				} else {
					stream := connMethodNode(driver, "stream", q.ConstantName, q.ArgDictNode())
					f.Body = append(f.Body,
						assignNode("result", poet.Await(stream)),
						poet.Node(
							&pyast.AsyncFor{
								Target: poet.Name("row"),
								Iter:   poet.Name("result"),
								Body: []*pyast.Node{
									poet.Expr(
										poet.Yield(
											q.Ret.RowNode("row"),
										),
									),
								},
							},
						),
					)
				}
				f.Returns = subscriptNode("AsyncIterator", q.Ret.Annotation())
			case ":exec":
				f.Body = append(f.Body, poet.Await(exec))
				f.Returns = poet.Constant(nil)
			case ":execrows":
				f.Body = append(f.Body, execRowsBody(driver, q, exec, true)...)
				f.Returns = poet.Name("int")
			case ":execresult":
				f.Body = append(f.Body,
					poet.Return(poet.Await(exec)),
				)
				if driver == driverDBAPI {
					f.Returns = poet.Name("_DBAPICursor")
				} else {
					f.Returns = typeRefNode("sqlalchemy", "engine", "Result")
				}
			default:
				panic("unknown cmd " + q.Cmd)
			}

			cls.Body = append(cls.Body, poet.Node(f))
		}
		mod.Body = append(mod.Body, poet.Node(cls))
	}

	return poet.Node(mod)
}

type pyTmplCtx struct {
	SqlcVersion string
	Models      []Struct
	Queries     []Query
	Enums       []Enum
	SourceName  string
	C           Config
}

func (t *pyTmplCtx) OutputQuery(sourceName string) bool {
	return t.SourceName == sourceName
}

func HashComment(s string) string {
	return "# " + strings.ReplaceAll(s, "\n", "\n# ")
}

func Generate(_ context.Context, req *plugin.GenerateRequest) (*plugin.GenerateResponse, error) {
	var conf Config
	if len(req.PluginOptions) > 0 {
		if err := json.Unmarshal(req.PluginOptions, &conf); err != nil {
			return nil, err
		}
	}
	switch conf.driver() {
	case driverSQLAlchemy:
		if req.Settings.Engine == "sqlite" {
			return nil, fmt.Errorf("the sqlite engine requires driver %q", driverDBAPI)
		}
	case driverDBAPI:
		if req.Settings.Engine != "sqlite" {
			return nil, fmt.Errorf("driver %q only supports the sqlite engine", driverDBAPI)
		}
	default:
		return nil, fmt.Errorf("unsupported Python driver %q (expected %q or %q)", conf.Driver, driverSQLAlchemy, driverDBAPI)
	}

	enums := buildEnums(req)
	models := buildModels(conf, req)
	queries, err := buildQueries(conf, req, models)
	if err != nil {
		return nil, err
	}

	i := &importer{
		Models:  models,
		Queries: queries,
		Enums:   enums,
		C:       conf,
	}

	tctx := pyTmplCtx{
		Models:      models,
		Queries:     queries,
		Enums:       enums,
		SqlcVersion: req.SqlcVersion,
		C:           conf,
	}

	output := map[string]string{}
	result := pyprint.Print(buildModelsTree(&tctx, i), pyprint.Options{})
	tctx.SourceName = "models.py"
	output["models.py"] = string(result.Python)

	files := map[string]struct{}{}
	for _, q := range queries {
		files[q.SourceName] = struct{}{}
	}

	for source := range files {
		tctx.SourceName = source
		result := pyprint.Print(buildQueryTree(&tctx, i, source), pyprint.Options{})
		name := source
		if !strings.HasSuffix(name, ".py") {
			name = strings.TrimSuffix(name, ".sql")
			name += ".py"
		}
		output[name] = string(result.Python)
	}

	resp := plugin.GenerateResponse{}

	for filename, code := range output {
		resp.Files = append(resp.Files, &plugin.File{
			Name:     filename,
			Contents: []byte(code),
		})
	}

	return &resp, nil
}
