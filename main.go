package main

import (
	"bufio"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// TODO: FIX DUPLICATE ENUM VALUES THAT CAUSE ERROR (duplicate key 1 in map literal)
// usage go run main.go -in sap-metadata.xml -out models_gen.go -pkg models

type Options struct {
	PkgName      string
	DecimalMode  string // "shopspring" or "string"
	NsPrefixMode string // "auto", "always", "none"
	InPath       string
	OutPath      string
}

func main() {
	opts := parseFlags()
	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func strconvQuote(s string) string { return strconv.Quote(s) }

func parseFlags() Options {
	var opts Options
	flag.StringVar(&opts.InPath, "in", "", "input metadata XML file (default: stdin)")
	flag.StringVar(&opts.OutPath, "out", "",
		"output Go file (default: stdout)")
	flag.StringVar(&opts.PkgName, "pkg", "models", "package name for generated code")
	flag.StringVar(&opts.DecimalMode, "decimal", "shopspring",
		"decimal mode: shopspring | string")
	flag.StringVar(&opts.NsPrefixMode, "ns-prefix", "auto",
		"namespace prefix mode: auto | always | none")
	flag.Parse()
	opts.DecimalMode = strings.ToLower(opts.DecimalMode)
	switch opts.DecimalMode {
	case "shopspring", "string":
	default:
		fmt.Fprintf(os.Stderr,
			"warning: -decimal=%q invalid; falling back to shopspring\n",
			opts.DecimalMode)
		opts.DecimalMode = "shopspring"
	}
	opts.NsPrefixMode = strings.ToLower(opts.NsPrefixMode)
	switch opts.NsPrefixMode {
	case "auto", "always", "none":
	default:
		fmt.Fprintf(os.Stderr,
			"warning: -ns-prefix=%q invalid; falling back to auto\n",
			opts.NsPrefixMode)
		opts.NsPrefixMode = "auto"
	}
	return opts
}

func run(opts Options) error {
	in := os.Stdin
	var err error
	if opts.InPath != "" {
		in, err = os.Open(opts.InPath)
		if err != nil {
			return fmt.Errorf("open input: %w", err)
		}
		defer in.Close()
	}

	schemas, err := parseEdmx(in)
	if err != nil {
		return fmt.Errorf("parse metadata: %w", err)
	}
	if len(schemas) == 0 {
		return errors.New("no <Schema> found in metadata")
	}

	gen, err := generate(schemas, opts)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	out := os.Stdout
	if opts.OutPath != "" {
		out, err = os.Create(opts.OutPath)
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		defer out.Close()
	}
	w := bufio.NewWriter(out)
	if _, err := w.WriteString(gen); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return w.Flush()
}

/* ===========================
   Metadata model structures
   =========================== */

type Schema struct {
	Namespace    string
	EntityTypes  map[string]*EntityType
	ComplexTypes map[string]*ComplexType
	EnumTypes    map[string]*EnumType
	Associations map[string]*Association // v3 only
}

type EntityType struct {
	Namespace  string
	Name       string
	BaseType   string // qualified or ""
	Keys       []string
	Properties []*Property
	NavPropsV4 []*NavPropertyV4
	NavPropsV3 []*NavPropertyV3 // v3 only
}

type ComplexType struct {
	Namespace  string
	Name       string
	BaseType   string // qualified or ""
	Properties []*Property
}

type EnumType struct {
	Namespace  string
	Name       string
	Underlying string // Edm.Int32 etc (v4) or ""
	Members    []EnumMember
	IsFlags    bool // support flags enums (comma-separated names)
}

type EnumMember struct {
	Name  string
	Value string // keep as string to support non-int32 too
}

type Property struct {
	Name     string
	Type     string // Edm.* or Qualified.Type or Collection(...)
	Nullable *bool
}

type NavPropertyV4 struct {
	Name     string
	Type     string // Collection(NS.Type) or NS.Type
	Nullable *bool
}

type NavPropertyV3 struct {
	Name         string
	Relationship string // NS.Association
	FromRole     string
	ToRole       string
}

type Association struct {
	Namespace string
	Name      string
	Ends      []AssocEnd
}

type AssocEnd struct {
	Role         string
	Type         string // NS.Entity
	Multiplicity string // "*", "0..1", "1"
}

/* ===========================
   XML parsing (v3 and v4)
   =========================== */

func parseEdmx(r io.Reader) ([]*Schema, error) {
	dec := xml.NewDecoder(r)
	var schemas []*Schema
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == "Schema" {
			s, err := parseSchema(dec, se)
			if err != nil {
				return nil, err
			}
			schemas = append(schemas, s)
		}
	}
	return schemas, nil
}

func parseSchema(dec *xml.Decoder, start xml.StartElement) (*Schema, error) {
	s := &Schema{
		Namespace:    attr(start, "Namespace"),
		EntityTypes:  map[string]*EntityType{},
		ComplexTypes: map[string]*ComplexType{},
		EnumTypes:    map[string]*EnumType{},
		Associations: map[string]*Association{},
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			switch tt.Name.Local {
			case "EntityType":
				e, err := parseEntityType(dec, tt, s.Namespace)
				if err != nil {
					return nil, err
				}
				s.EntityTypes[e.Name] = e
			case "ComplexType":
				c, err := parseComplexType(dec, tt, s.Namespace)
				if err != nil {
					return nil, err
				}
				s.ComplexTypes[c.Name] = c
			case "EnumType":
				e, err := parseEnumType(dec, tt, s.Namespace)
				if err != nil {
					return nil, err
				}
				s.EnumTypes[e.Name] = e
			case "Association": // v3
				a, err := parseAssociation(dec, tt, s.Namespace)
				if err != nil {
					return nil, err
				}
				s.Associations[a.Name] = a
			default:
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return s, nil
			}
		}
	}
}

func parseEntityType(
	dec *xml.Decoder,
	start xml.StartElement,
	ns string,
) (*EntityType, error) {
	e := &EntityType{
		Namespace:  ns,
		Name:       attr(start, "Name"),
		BaseType:   attr(start, "BaseType"),
		Properties: []*Property{},
		NavPropsV4: []*NavPropertyV4{},
		NavPropsV3: []*NavPropertyV3{},
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			switch tt.Name.Local {
			case "Key":
				ks, err := parseKey(dec, tt)
				if err != nil {
					return nil, err
				}
				e.Keys = ks
			case "Property":
				p := parseProperty(tt)
				e.Properties = append(e.Properties, &p)
				if err := skip(dec, "Property"); err != nil {
					return nil, err
				}
			case "NavigationProperty":
				// v4 has Type attribute; v3 has Relationship attribute
				if hasAttr(tt, "Type") {
					np := parseNavPropV4(tt)
					e.NavPropsV4 = append(e.NavPropsV4, &np)
					if err := skip(dec, "NavigationProperty"); err != nil {
						return nil, err
					}
				} else {
					np := parseNavPropV3(tt)
					e.NavPropsV3 = append(e.NavPropsV3, &np)
					if err := skip(dec, "NavigationProperty"); err != nil {
						return nil, err
					}
				}
			default:
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return e, nil
			}
		}
	}
}

func parseComplexType(
	dec *xml.Decoder,
	start xml.StartElement,
	ns string,
) (*ComplexType, error) {
	c := &ComplexType{
		Namespace:  ns,
		Name:       attr(start, "Name"),
		BaseType:   attr(start, "BaseType"),
		Properties: []*Property{},
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			switch tt.Name.Local {
			case "Property":
				p := parseProperty(tt)
				c.Properties = append(c.Properties, &p)
				if err := skip(dec, "Property"); err != nil {
					return nil, err
				}
			default:
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return c, nil
			}
		}
	}
}

func parseEnumType(
	dec *xml.Decoder,
	start xml.StartElement,
	ns string,
) (*EnumType, error) {
	e := &EnumType{
		Namespace:  ns,
		Name:       attr(start, "Name"),
		Underlying: attr(start, "UnderlyingType"), // v4
		IsFlags:    strings.EqualFold(attr(start, "IsFlags"), "true"),
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			if tt.Name.Local == "Member" {
				m := EnumMember{
					Name:  attr(tt, "Name"),
					Value: attr(tt, "Value"),
				}
				e.Members = append(e.Members, m)
				if err := skip(dec, "Member"); err != nil {
					return nil, err
				}
			} else {
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return e, nil
			}
		}
	}
}

func parseAssociation(
	dec *xml.Decoder,
	start xml.StartElement,
	ns string,
) (*Association, error) {
	a := &Association{
		Namespace: ns,
		Name:      attr(start, "Name"),
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			if tt.Name.Local == "End" {
				end := AssocEnd{
					Role:         attr(tt, "Role"),
					Type:         attr(tt, "Type"),
					Multiplicity: attr(tt, "Multiplicity"),
				}
				a.Ends = append(a.Ends, end)
				if err := skip(dec, "End"); err != nil {
					return nil, err
				}
			} else {
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return a, nil
			}
		}
	}
}

func parseKey(dec *xml.Decoder, start xml.StartElement) ([]string, error) {
	var keys []string
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			if tt.Name.Local == "PropertyRef" {
				n := attr(tt, "Name")
				if n != "" {
					keys = append(keys, n)
				}
				if err := skip(dec, "PropertyRef"); err != nil {
					return nil, err
				}
			} else {
				if err := skip(dec, tt.Name.Local); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if tt.Name.Local == start.Name.Local {
				return keys, nil
			}
		}
	}
}

func parseProperty(start xml.StartElement) Property {
	p := Property{
		Name:     attr(start, "Name"),
		Type:     attr(start, "Type"),
		Nullable: parseNullablePtr(attr(start, "Nullable")),
	}
	return p
}

func parseNavPropV4(start xml.StartElement) NavPropertyV4 {
	return NavPropertyV4{
		Name:     attr(start, "Name"),
		Type:     attr(start, "Type"),
		Nullable: parseNullablePtr(attr(start, "Nullable")),
	}
}

func parseNavPropV3(start xml.StartElement) NavPropertyV3 {
	return NavPropertyV3{
		Name:         attr(start, "Name"),
		Relationship: attr(start, "Relationship"),
		FromRole:     attr(start, "FromRole"),
		ToRole:       attr(start, "ToRole"),
	}
}

func attr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func hasAttr(se xml.StartElement, name string) bool {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return true
		}
	}
	return false
}
func skip(dec *xml.Decoder, local string) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tt := tok.(type) {
		case xml.StartElement:
			depth++
		case xml.EndElement:
			if tt.Name.Local == local {
				depth--
			} else {
				depth--
			}
		}
	}
	return nil
}
func parseNullablePtr(v string) *bool {
	if v == "" {
		return nil
	}
	b := strings.EqualFold(v, "true")
	if strings.EqualFold(v, "false") {
		b = false
	}
	return &b
}

/* ===========================
   Code generation
   =========================== */

type genState struct {
	opts          Options
	schemas       []*Schema
	nsAliases     map[string]string
	useTime       bool
	useDecimal    bool
	decimalImport string                  // "github.com/shopspring/decimal"
	knownTypes    map[string]bool         // qualified "NS.Name"
	typeNameMap   map[string]string       // qualified -> GoTypeName
	assocByQName  map[string]*Association // "NS.Assoc"
	useJSON       bool
	useFmt        bool
	useStrings    bool
}

func generate(schemas []*Schema, opts Options) (string, error) {
	st := &genState{
		opts:          opts,
		schemas:       schemas,
		nsAliases:     map[string]string{},
		knownTypes:    map[string]bool{},
		typeNameMap:   map[string]string{},
		assocByQName:  map[string]*Association{},
		decimalImport: "github.com/shopspring/decimal",
	}

	// Build list of namespaces and decide aliasing
	nsList := distinctNamespaces(schemas)
	needPrefix := opts.NsPrefixMode == "always" ||
		(opts.NsPrefixMode == "auto" && len(nsList) > 1)
	for _, ns := range nsList {
		alias := namespaceAlias(ns)
		st.nsAliases[ns] = alias
	}

	// Register known types and associations
	for _, s := range schemas {
		for name := range s.EntityTypes {
			qn := s.Namespace + "." + name
			st.knownTypes[qn] = true
		}
		for name := range s.ComplexTypes {
			qn := s.Namespace + "." + name
			st.knownTypes[qn] = true
		}
		for name := range s.EnumTypes {
			qn := s.Namespace + "." + name
			st.knownTypes[qn] = true
		}
		for name, a := range s.Associations {
			st.assocByQName[s.Namespace+"."+name] = a
		}
	}

	// Compute Go type names for qualified types
	// If needPrefix, always prepend ns alias; else only if conflicts
	conflictNames := map[string]int{}
	for qn := range st.knownTypes {
		base := baseNameFromQualified(qn)
		conflictNames[base]++
	}
	for qn := range st.knownTypes {
		ns, base := splitQualified(qn)
		goName := goExported(base)
		if needPrefix || conflictNames[base] > 1 {
			goName = st.nsAliases[ns] + goName
		}
		st.typeNameMap[qn] = goName
	}

	// Start generating
	var b strings.Builder
	b.WriteString("// Code generated by odata2go. DO NOT EDIT.\n")
	b.WriteString("// Source: OData metadata (Edmx)\n\n")
	b.WriteString("package " + opts.PkgName + "\n\n")

	// Imports determined later; collect types first
	var typeBlocks []string

	// Enums
	enumKeys := []string{}
	for _, s := range schemas {
		for name := range s.EnumTypes {
			enumKeys = append(enumKeys, s.Namespace+"."+name)
		}
	}
	sort.Strings(enumKeys)
	for _, qn := range enumKeys {
		ns, name := splitQualified(qn)
		e := findEnum(schemas, ns, name)
		if e == nil {
			continue
		}
		typeDecl := st.emitEnum(e)
		typeBlocks = append(typeBlocks, typeDecl)
	}

	// Complex types first (often used in entities)
	compKeys := []string{}
	for _, s := range schemas {
		for name := range s.ComplexTypes {
			compKeys = append(compKeys, s.Namespace+"."+name)
		}
	}
	sort.Strings(compKeys)
	for _, qn := range compKeys {
		ns, name := splitQualified(qn)
		c := findComplex(schemas, ns, name)
		if c == nil {
			continue
		}
		typeDecl := st.emitComplex(c)
		typeBlocks = append(typeBlocks, typeDecl)
	}

	// Entity types
	entKeys := []string{}
	for _, s := range schemas {
		for name := range s.EntityTypes {
			entKeys = append(entKeys, s.Namespace+"."+name)
		}
	}
	sort.Strings(entKeys)
	for _, qn := range entKeys {
		ns, name := splitQualified(qn)
		e := findEntity(schemas, ns, name)
		if e == nil {
			continue
		}
		typeDecl := st.emitEntity(e)
		typeBlocks = append(typeBlocks, typeDecl)
	}

	// Imports
	imports := st.collectImports()
	if len(imports) > 0 {
		b.WriteString("import (\n")
		for _, imp := range imports {
			b.WriteString("  \"" + imp + "\"\n")
		}
		b.WriteString(")\n\n")
	}

	// Write types
	for _, block := range typeBlocks {
		b.WriteString(block)
		if !strings.HasSuffix(block, "\n") {
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

func (st *genState) collectImports() []string {
	set := map[string]bool{}
	if st.useTime {
		set["time"] = true
	}
	if st.useDecimal && st.opts.DecimalMode == "shopspring" {
		set[st.decimalImport] = true
	}
	if st.useJSON {
		set["encoding/json"] = true
	}
	if st.useFmt {
		set["fmt"] = true
	}
	if st.useStrings {
		set["strings"] = true
	}
	keys := []string{}
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (st *genState) emitEnum(e *EnumType) string {
	qn := e.Namespace + "." + e.Name
	goName := st.typeNameMap[qn]
	underlying := e.Underlying
	if underlying == "" {
		underlying = "Edm.Int32"
	}
	goUnder, _, _ := st.mapEdmToGo(underlying, false)
	if goUnder == "" {
		goUnder = "int32"
	}
	// We will emit JSON marshal/unmarshal methods
	st.useJSON = true
	st.useFmt = true
	if e.IsFlags {
		st.useStrings = true
	}
	var b strings.Builder
	b.WriteString("// " + goName + " is an enum from OData.\n")
	b.WriteString("type " + goName + " " + goUnder + "\n\n")
	if len(e.Members) > 0 {
		b.WriteString("const (\n")
		for idx, m := range e.Members {
			constName := goName + goExported(m.Name)
			if m.Value != "" {
				b.WriteString("  " + constName + " " + goName + " = " +
					castEnumValue(goUnder, m.Value) + "\n")
			} else {
				// Sequential if no values; use iota
				if idx == 0 {
					b.WriteString("  " + constName + " " + goName + " = iota\n")
				} else {
					b.WriteString("  " + constName + "\n")
				}
			}
		}
		b.WriteString(")\n\n")
	}
	// name <-> value maps
	b.WriteString("var _" + goName + "_nameToValue = map[string]" + goName + "{\n")
	for _, m := range e.Members {
		constName := goName + goExported(m.Name)
		b.WriteString("  " + strconvQuote(m.Name) + ": " + constName + ",\n")
	}
	b.WriteString("}\n\n")

	b.WriteString("var _" + goName + "_valueToName = map[" + goName + "]string{\n")
	for _, m := range e.Members {
		constName := goName + goExported(m.Name)
		b.WriteString("  " + constName + ": " + strconvQuote(m.Name) + ",\n")
	}
	b.WriteString("}\n\n")

	// UnmarshalJSON supports both string names and numeric values.
	b.WriteString("func (t *" + goName + ") UnmarshalJSON(b []byte) error {\n")
	b.WriteString("  if string(b) == \"null\" {\n")
	b.WriteString("    *t = 0\n")
	b.WriteString("    return nil\n")
	b.WriteString("  }\n")
	b.WriteString("  if len(b) > 0 && b[0] == '\"' {\n")
	b.WriteString("    var s string\n")
	b.WriteString("    if err := json.Unmarshal(b, &s); err != nil { return err }\n")
	if e.IsFlags {
		b.WriteString("    if s == \"\" { *t = 0; return nil }\n")
		b.WriteString("    var v " + goName + "\n")
		b.WriteString("    for _, p := range strings.Split(s, \",\") {\n")
		b.WriteString("      p = strings.TrimSpace(p)\n")
		b.WriteString("      if p == \"\" { continue }\n")
		b.WriteString("      mv, ok := _" + goName + "_nameToValue[p]\n")
		b.WriteString("      if !ok { return fmt.Errorf(\"invalid " + goName + " value %q\", p) }\n")
		b.WriteString("      v |= mv\n")
		b.WriteString("    }\n")
		b.WriteString("    *t = v\n")
		b.WriteString("    return nil\n")
	} else {
		b.WriteString("    if v, ok := _" + goName + "_nameToValue[s]; ok {\n")
		b.WriteString("      *t = v\n")
		b.WriteString("      return nil\n")
		b.WriteString("    }\n")
		b.WriteString("    return fmt.Errorf(\"invalid " + goName + " value %q\", s)\n")
	}
	b.WriteString("  }\n")
	b.WriteString("  var n " + goUnder + "\n")
	b.WriteString("  if err := json.Unmarshal(b, &n); err != nil { return err }\n")
	b.WriteString("  *t = " + goName + "(n)\n")
	b.WriteString("  return nil\n")
	b.WriteString("}\n\n")

	// MarshalJSON emits the string name when known; numeric otherwise.
	b.WriteString("func (t " + goName + ") MarshalJSON() ([]byte, error) {\n")
	b.WriteString("  if s, ok := _" + goName + "_valueToName[t]; ok {\n")
	b.WriteString("    return json.Marshal(s)\n")
	b.WriteString("  }\n")
	b.WriteString("  n := " + goUnder + "(t)\n")
	b.WriteString("  return json.Marshal(n)\n")
	b.WriteString("}\n\n")

	// String returns the enum name if known; otherwise the numeric value.
	b.WriteString("func (t " + goName + ") String() string {\n")
	b.WriteString("  if s, ok := _" + goName + "_valueToName[t]; ok { return s }\n")
	b.WriteString("  return fmt.Sprintf(\"%d\", " + goUnder + "(t))\n")
	b.WriteString("}\n\n")
	return b.String()
}

func castEnumValue(goUnder string, value string) string {
	// Emit as literal, default to int parse
	if strings.HasPrefix(goUnder, "int") ||
		strings.HasPrefix(goUnder, "uint") ||
		strings.HasPrefix(goUnder, "byte") {
		return value
	}
	// Fallback: wrap in type conversion (string enums are rare)
	return value
}

func (st *genState) emitComplex(c *ComplexType) string {
	qn := c.Namespace + "." + c.Name
	goName := st.typeNameMap[qn]
	var b strings.Builder
	b.WriteString("// " + goName + " is a complex type.\n")
	b.WriteString("type " + goName + " struct {\n")
	// Embed base type if present
	if c.BaseType != "" && st.knownTypes[c.BaseType] {
		bName := st.typeNameMap[c.BaseType]
		b.WriteString("  " + bName + "\n")
	}
	// Properties
	for _, p := range c.Properties {
		field := st.fieldForProperty(p, c.Namespace)
		b.WriteString("  " + field + "\n")
	}
	b.WriteString("}\n\n")
	return b.String()
}

func (st *genState) emitEntity(e *EntityType) string {
	qn := e.Namespace + "." + e.Name
	goName := st.typeNameMap[qn]
	var b strings.Builder
	b.WriteString("// " + goName + " is an entity type.\n")
	b.WriteString("type " + goName + " struct {\n")
	// Embed base type if present
	if e.BaseType != "" && st.knownTypes[e.BaseType] {
		bName := st.typeNameMap[e.BaseType]
		b.WriteString("  " + bName + "\n")
	}
	// Properties
	keySet := map[string]bool{}
	for _, k := range e.Keys {
		keySet[k] = true
	}
	for _, p := range e.Properties {
		field := st.fieldForPropertyWithKey(p, e.Namespace, keySet)
		b.WriteString("  " + field + "\n")
	}
	// Navigation properties
	for _, np := range e.NavPropsV4 {
		field := st.fieldForNavV4(np, e.Namespace)
		b.WriteString("  " + field + "\n")
	}
	for _, np := range e.NavPropsV3 {
		field := st.fieldForNavV3(np, e.Namespace)
		b.WriteString("  " + field + "\n")
	}
	b.WriteString("}\n\n")
	return b.String()
}

/* ===========================
   Field generation helpers
   =========================== */

func (st *genState) fieldForProperty(p *Property, ctxNS string) string {
	return st.fieldForPropertyWithKey(p, ctxNS, nil)
}

func (st *genState) fieldForPropertyWithKey(
	p *Property,
	ctxNS string,
	keySet map[string]bool,
) string {
	fieldName := safeFieldName(p.Name)
	// Resolve type
	goType := st.resolveTypeRef(p.Type, p.Nullable, ctxNS)
	tags := []string{`json:"` + p.Name + `,omitempty"`}
	if keySet != nil && keySet[p.Name] {
		tags = append(tags, `key:"true"`)
	}
	return fmt.Sprintf("%s %s `%s`",
		fieldName, goType, strings.Join(tags, " "))
}

func (st *genState) fieldForNavV4(
	np *NavPropertyV4,
	ctxNS string,
) string {
	fieldName := safeFieldName(np.Name)
	goType := st.resolveTypeRef(np.Type, np.Nullable, ctxNS)
	tag := `json:"` + np.Name + `,omitempty"`
	return fmt.Sprintf("%s %s `%s`", fieldName, goType, tag)
}

func (st *genState) fieldForNavV3(
	np *NavPropertyV3,
	ctxNS string,
) string {
	fieldName := safeFieldName(np.Name)
	// Resolve via Association
	// Relationship is NS.Association
	assoc := st.assocByQName[np.Relationship]
	elemType := "interface{}"
	isCollection := false
	if assoc != nil {
		// Find the ToRole end
		var target AssocEnd
		for _, end := range assoc.Ends {
			if end.Role == np.ToRole {
				target = end
				break
			}
		}
		if target.Type != "" {
			elemType = st.resolveTypeRef(target.Type, nil, ctxNS)
			// target.Type is qualified NS.Entity, not a Collection
			// Determine multiplicity for collection
			if target.Multiplicity == "*" || target.Multiplicity == "0..*" {
				isCollection = true
			} else {
				isCollection = false
			}
		}
	}
	var goType string
	if isCollection {
		// Slice of element type (strip pointer for collection)
		elem := stripPointer(elemType)
		goType = "[]" + elem
	} else {
		// Single; pointer to struct types
		if !strings.HasPrefix(elemType, "*") &&
			isStructNamedType(elemType) {
			goType = "*" + elemType
		} else {
			goType = elemType
		}
	}
	tag := `json:"` + np.Name + `,omitempty"`
	return fmt.Sprintf("%s %s `%s`", fieldName, goType, tag)
}

func stripPointer(t string) string {
	if strings.HasPrefix(t, "*") {
		return strings.TrimPrefix(t, "*")
	}
	return t
}

func isStructNamedType(t string) bool {
	// crude heuristic: named type without [] or map or basic
	if strings.HasPrefix(t, "[]") || strings.HasPrefix(t, "map[") {
		return false
	}
	basic := map[string]bool{
		"string": true, "bool": true, "byte": true, "rune": true,
		"int": true, "int8": true, "int16": true, "int32": true,
		"int64": true, "uint": true, "uint8": true, "uint16": true,
		"uint32": true, "uint64": true, "float32": true, "float64": true,
		"time.Time": true, "json.RawMessage": true,
	}
	if basic[t] {
		return false
	}
	// decimal.Decimal is struct named type
	if t == "decimal.Decimal" {
		return true
	}
	// Assume other non-basic names are struct types
	return true
}

var reCollection = regexp.MustCompile(`^Collection\((.+)\)$`)

func (st *genState) resolveTypeRef(
	raw string,
	nullable *bool,
	ctxNS string,
) string {
	// Collection(...)
	if m := reCollection.FindStringSubmatch(raw); len(m) == 2 {
		inner := st.resolveTypeRef(m[1], nil, ctxNS)
		// For collections of complex/entity, use slice of concrete (no pointer)
		return "[]" + stripPointer(inner)
	}

	// Edm.* primitives
	if strings.HasPrefix(raw, "Edm.") {
		t, needsTime, needsDec := st.mapEdmToGo(raw, boolOrDefault(nullable, true))
		if needsTime {
			st.useTime = true
		}
		if needsDec {
			st.useDecimal = true
		}
		return t
	}

	// Qualified "NS.Type" or maybe unqualified type: assume ctxNS
	qn := raw
	if !strings.Contains(raw, ".") {
		qn = ctxNS + "." + raw
	}
	goName := st.typeNameMap[qn]
	if goName == "" {
		// Unknown type; fallback
		return "interface{}"
	}

	// Nullability: for non-collection and non-basic, pointer for nullable
	isNullable := boolOrDefault(nullable, true)
	if isNullable {
		// For named struct types, prefer pointer
		if isStructNamedType(goName) {
			return "*" + goName
		}
	}
	return goName
}

func (st *genState) mapEdmToGo(
	edm string,
	nullable bool,
) (goType string, needsTime bool, needsDecimal bool) {
	switch edm {
	case "Edm.String":
		if nullable {
			return "*string", false, false
		}
		return "string", false, false
	case "Edm.Boolean":
		if nullable {
			return "*bool", false, false
		}
		return "bool", false, false
	case "Edm.Byte":
		if nullable {
			return "*uint8", false, false
		}
		return "uint8", false, false
	case "Edm.SByte":
		if nullable {
			return "*int8", false, false
		}
		return "int8", false, false
	case "Edm.Int16":
		if nullable {
			return "*int16", false, false
		}
		return "int16", false, false
	case "Edm.Int32":
		if nullable {
			return "*int32", false, false
		}
		return "int32", false, false
	case "Edm.Int64":
		if nullable {
			return "*int64", false, false
		}
		return "int64", false, false
	case "Edm.Single":
		if nullable {
			return "*float32", false, false
		}
		return "float32", false, false
	case "Edm.Double":
		if nullable {
			return "*float64", false, false
		}
		return "float64", false, false
	case "Edm.Decimal":
		if st.opts.DecimalMode == "string" {
			if nullable {
				return "*string", false, false
			}
			return "string", false, false
		}
		// shopspring/decimal
		if nullable {
			return "*decimal.Decimal", false, true
		}
		return "decimal.Decimal", false, true
	case "Edm.Date", "Edm.DateTime", "Edm.DateTimeOffset":
		// Use time.Time for all date/time
		return "time.Time", true, false
	case "Edm.TimeOfDay", "Edm.Time": // v4 time-only or v3 duration-like
		// Represent as string "HH:MM:SS[.fffffff]"
		if nullable {
			return "*string", false, false
		}
		return "string", false, false
	case "Edm.Duration":
		// ISO 8601 duration e.g., "P3D"; keep as string
		if nullable {
			return "*string", false, false
		}
		return "string", false, false
	case "Edm.Binary":
		// JSON payload is base64; []byte works with encoding/json
		return "[]byte", false, false
	case "Edm.Guid":
		if nullable {
			return "*string", false, false
		}
		return "string", false, false
	default:
		// Geospatial and others: fallback to string
		if nullable {
			return "*string", false, false
		}
		return "string", false, false
	}
}

/* ===========================
   Lookups and utilities
   =========================== */

func findEntity(schemas []*Schema, ns, name string) *EntityType {
	for _, s := range schemas {
		if s.Namespace == ns {
			if e := s.EntityTypes[name]; e != nil {
				return e
			}
		}
	}
	return nil
}
func findComplex(schemas []*Schema, ns, name string) *ComplexType {
	for _, s := range schemas {
		if s.Namespace == ns {
			if c := s.ComplexTypes[name]; c != nil {
				return c
			}
		}
	}
	return nil
}
func findEnum(schemas []*Schema, ns, name string) *EnumType {
	for _, s := range schemas {
		if s.Namespace == ns {
			if e := s.EnumTypes[name]; e != nil {
				return e
			}
		}
	}
	return nil
}

func distinctNamespaces(schemas []*Schema) []string {
	set := map[string]bool{}
	for _, s := range schemas {
		if s.Namespace != "" {
			set[s.Namespace] = true
		}
	}
	out := []string{}
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func namespaceAlias(ns string) string {
	// Use last segment after '.' or '/'
	seg := ns
	if idx := strings.LastIndex(ns, "."); idx >= 0 {
		seg = ns[idx+1:]
	}
	if idx := strings.LastIndex(seg, "/"); idx >= 0 {
		seg = seg[idx+1:]
	}
	seg = sanitizeIdent(seg)
	if seg == "" {
		seg = "NS"
	}
	// Ensure exported and CamelCase
	return goExported(seg)
}

func baseNameFromQualified(qn string) string {
	_, base := splitQualified(qn)
	return base
}

func splitQualified(qn string) (ns, name string) {
	idx := strings.LastIndex(qn, ".")
	if idx < 0 {
		return "", qn
	}
	return qn[:idx], qn[idx+1:]
}

func goExported(name string) string {
	if name == "" {
		return "X"
	}
	// Keep underscores as-is for SAP U_ fields
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	// Remove invalid characters
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if i == 0 {
			if unicode.IsLetter(r) || r == '_' {
				out = append(out, r)
			} else {
				out = append(out, 'X')
				if unicode.IsDigit(r) {
					out = append(out, r)
				}
			}
		} else {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				out = append(out, r)
			}
		}
	}
	return string(out)
}

func sanitizeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func safeFieldName(name string) string {
	n := goExported(name)
	// Avoid Go keywords by suffixing underscore
	switch n {
	case "break", "default", "func", "interface", "select",
		"case", "defer", "go", "map", "struct",
		"chan", "else", "goto", "package", "switch",
		"const", "fallthrough", "if", "range", "type",
		"continue", "for", "import", "return", "var":
		return n + "_"
	}
	return n
}

func boolOrDefault(p *bool, d bool) bool {
	if p == nil {
		return d
	}
	return *p
}
