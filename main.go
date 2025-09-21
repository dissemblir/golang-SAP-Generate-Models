package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Usage examples:
//   Split per type (recommended):
//     go run main.go -input="metadata.xml" -outDir="./types" -split="perType"
//   Single file (legacy):
//     go run main.go -input="metadata.xml" -output="types.ts" -split="single"

// (Same XML parsing structs as before - unchanged for SAP B1 compatibility)
type EDMX struct {
	XMLName      xml.Name     `xml:"http://docs.oasis-open.org/odata/ns/edmx Edmx"`
	Version      string       `xml:"Version,attr"`
	DataServices DataServices `xml:"http://docs.oasis-open.org/odata/ns/edmx DataServices"`
}

type DataServices struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edmx DataServices"`
	Schemas []Schema `xml:"http://docs.oasis-open.org/odata/ns/edm Schema"`
}

type Schema struct {
	XMLName          xml.Name          `xml:"http://docs.oasis-open.org/odata/ns/edm Schema"`
	Namespace        string            `xml:"Namespace,attr"`
	Alias            string            `xml:"Alias,attr,omitempty"`
	EntityTypes      []EntityType      `xml:"http://docs.oasis-open.org/odata/ns/edm EntityType"`
	ComplexTypes     []ComplexType     `xml:"http://docs.oasis-open.org/odata/ns/edm ComplexType"`
	EnumTypes        []EnumType        `xml:"http://docs.oasis-open.org/odata/ns/edm EnumType"`
	EntityContainers []EntityContainer `xml:"http://docs.oasis-open.org/odata/ns/edm EntityContainer,omitempty"`
}

type EntityType struct {
	XMLName              xml.Name             `xml:"http://docs.oasis-open.org/odata/ns/edm EntityType"`
	Name                 string               `xml:"Name,attr"`
	Key                  []PropertyRef        `xml:"http://docs.oasis-open.org/odata/ns/edm Key>PropertyRef"`
	Properties           []Property           `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	NavigationProperties []NavigationProperty `xml:"http://docs.oasis-open.org/odata/ns/edm NavigationProperty"`
	Base                 string               `xml:"Base,attr,omitempty"`
}

type ComplexType struct {
	XMLName              xml.Name             `xml:"http://docs.oasis-open.org/odata/ns/edm ComplexType"`
	Name                 string               `xml:"Name,attr"`
	Properties           []Property           `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	NavigationProperties []NavigationProperty `xml:"http://docs.oasis-open.org/odata/ns/edm NavigationProperty"`
}

type EnumType struct {
	XMLName        xml.Name     `xml:"http://docs.oasis-open.org/odata/ns/edm EnumType"`
	Name           string       `xml:"Name,attr"`
	Flags          bool         `xml:"Flags,attr,omitempty"`
	IsFlags        bool         `xml:"IsFlags,attr,omitempty"`
	UnderlyingType string       `xml:"UnderlyingType,attr,omitempty"`
	Members        []EnumMember `xml:"http://docs.oasis-open.org/odata/ns/edm Member"`
}

type EnumMember struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm Member"`
	Name    string   `xml:"Name,attr"`
	Value   string   `xml:"Value,attr,omitempty"`
}

type Property struct {
	XMLName      xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	Name         string   `xml:"Name,attr"`
	Type         string   `xml:"Type,attr"`
	Nullable     bool     `xml:"Nullable,attr,omitempty"`
	MaxLength    int      `xml:"MaxLength,attr,omitempty"`
	Precision    int      `xml:"Precision,attr,omitempty"`
	Scale        int      `xml:"Scale,attr,omitempty"`
	DefaultValue string   `xml:"DefaultValue,attr,omitempty"`
}

type PropertyRef struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm PropertyRef"`
	Name    string   `xml:"Name,attr"`
}

type NavigationProperty struct {
	XMLName                xml.Name                `xml:"http://docs.oasis-open.org/odata/ns/edm NavigationProperty"`
	Name                   string                  `xml:"Name,attr"`
	Type                   string                  `xml:"Type,attr"`
	Partner                string                  `xml:"Partner,attr,omitempty"`
	ReferentialConstraints []ReferentialConstraint `xml:"http://docs.oasis-open.org/odata/ns/edm ReferentialConstraint,omitempty"`
}

type ReferentialConstraint struct {
	XMLName            xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm ReferentialConstraint"`
	Property           string   `xml:"Property,attr"`
	ReferencedProperty string   `xml:"ReferencedProperty,attr"`
}

type EntityContainer struct {
	XMLName    xml.Name    `xml:"http://docs.oasis-open.org/odata/ns/edm EntityContainer"`
	Name       string      `xml:"Name,attr"`
	EntitySets []EntitySet `xml:"http://docs.oasis-open.org/odata/ns/edm EntitySet,omitempty"`
}

type EntitySet struct {
	XMLName    xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm EntitySet"`
	Name       string   `xml:"Name,attr"`
	EntityType string   `xml:"EntityType,attr"`
}

// Type mappings from EDM primitive types to Zod (keys without "Edm." prefix).
var edmToZod = map[string]string{
	"String":         "z.string()",
	"Int16":          "z.number().int()",
	"Int32":          "z.number().int()",
	"Int64":          "z.number().int()",
	"Byte":           "z.number().int().nonnegative().max(255)",
	"SByte":          "z.number().int().min(-128).max(127)",
	"Boolean":        "z.boolean()",
	"Decimal":        "z.number()", // Or "z.string()" for precision
	"Double":         "z.number()",
	"Single":         "z.number()",
	"Guid":           "z.string().uuid()",
	"Date":           "z.coerce.date()", // Use coerce directly so it accepts both Date and ISO string inputs
	"DateTimeOffset": "z.coerce.date()",
	"TimeOfDay":      "z.string()", // TimeOfDay is typically "HH:MM:SS" and not reliably parseable by Date(), so keep it as string. (Change if your API returns full ISO timestamps.)
	"Binary":         "z.instanceof(Uint8Array)",
	"Stream":         "z.instanceof(Uint8Array)",
	"Duration":       "z.string()",
}

// Type mappings from EDM primitive types to TypeScript types.
// Align these with the final output type of your Zod pipelines.
var edmToTs = map[string]string{
	"String":         "string",
	"Int16":          "number",
	"Int32":          "number",
	"Int64":          "number",
	"Byte":           "number",
	"SByte":          "number",
	"Boolean":        "boolean",
	"Decimal":        "number", // or "string" if you prefer high precision
	"Double":         "number",
	"Single":         "number",
	"Guid":           "string",
	"Date":           "Date", // because of z.coerce.date()
	"DateTimeOffset": "Date",
	"TimeOfDay":      "Date", // mapped via coerce
	"Binary":         "Uint8Array",
	"Stream":         "Uint8Array",
	"Duration":       "string",
}

// Helper to extract the type name without namespace.
func extractEdmTypeName(edmType string) string {
	fullType := strings.TrimPrefix(edmType, "Collection(")
	fullType = strings.TrimSuffix(fullType, ")")
	split := strings.SplitN(fullType, ".", 2)
	if len(split) == 2 {
		return split[1] // Local name
	}
	return fullType
}

// Helper to determine if a type is collection and get inner type.
func isCollection(t string) (bool, string) {
	if strings.HasPrefix(t, "Collection(") && strings.HasSuffix(t, ")") {
		return true, t[11 : len(t)-1]
	}
	return false, t
}

// Get TypeScript type string for a given EDM type.
func getTsType(edmType string) string {
	isColl, innerEdm := isCollection(edmType)
	innerName := extractEdmTypeName(innerEdm)

	var baseTs string
	if ts, ok := edmToTs[innerName]; ok {
		baseTs = ts
	} else if innerName != "" {
		// Non-primitive: reference the generated friendly type (enum or complex/entity)
		baseTs = strings.Title(innerName)
	} else {
		baseTs = "unknown"
		log.Printf("Warning: Unknown TS type for '%s', using unknown", edmType)
	}

	if isColl {
		return baseTs + "[]"
	}
	return baseTs
}

// Get Zod type string for a given EDM type, wrapping refs in z.lazy for cycles/forward refs.
func getZodType(edmType string, isNullable bool, targetSchemaName string) string {
	isColl, innerEdm := isCollection(edmType)
	innerName := extractEdmTypeName(innerEdm)

	var baseZod string
	if zod, ok := edmToZod[innerName]; ok {
		baseZod = zod
	} else if innerName != "" {
		// Non-primitive: reference the schema (enum or complex/entity).
		if targetSchemaName == "" {
			targetSchemaName = innerName + "Schema"
		}
		baseZod = fmt.Sprintf("z.lazy(() => %s)", targetSchemaName)
	} else {
		baseZod = "z.unknown()"
		log.Printf("Warning: Unknown type '%s' for field, using z.unknown()", edmType)
	}

	if isColl {
		baseZod = fmt.Sprintf("z.array(%s)", baseZod)
		baseZod += ".nullish()"
		return baseZod
	}

	baseZod += ".nullish()"
	return baseZod
}

// Generate a TypeScript model type alias (used to break TS inference cycles).
// We generate NameModel instead of Name to preserve your existing export `type Name = z.infer<...>`
func generateTsModelType(typ interface{}) string {
	var name string
	var props []Property
	var navs []NavigationProperty

	switch t := typ.(type) {
	case EntityType:
		name = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	case ComplexType:
		name = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	}

	tsTypeName := strings.Title(name) + "Model"
	var b strings.Builder
	b.WriteString(fmt.Sprintf("export type %s = {\n", tsTypeName))

	// Scalar properties
	for _, p := range props {
		tsType := getTsType(p.Type)
		// Allow nulls commonly returned by the service: T | null, and property optional
		b.WriteString(fmt.Sprintf("  %s?: %s | null;\n", p.Name, tsType))
	}

	// Navigation properties
	for _, n := range navs {
		target := extractEdmTypeName(n.Type)
		targetTs := strings.Title(target)
		if isColl, _ := isCollection(n.Type); isColl {
			b.WriteString(fmt.Sprintf("  %s?: %s[] | null;\n", n.Name, targetTs))
		} else {
			b.WriteString(fmt.Sprintf("  %s?: %s | null;\n", n.Name, targetTs))
		}
	}

	b.WriteString("};\n\n")
	return b.String()
}

// Generate Zod schema for EntityType or ComplexType (as TS code string).
func generateZodSchema(typ interface{}, isEntity bool, schemaNs string) string {
	var name string
	var props []Property
	var navs []NavigationProperty

	switch t := typ.(type) {
	case EntityType:
		name = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	case ComplexType:
		name = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	}

	schemaName := strings.Title(name) + "Schema"
	tsModelName := strings.Title(name) + "Model"
	tsTypeName := strings.Title(name)

	var fields strings.Builder
	fields.WriteString(fmt.Sprintf("export const %s: ZodType<%s> = z.object({\n", schemaName, tsModelName))

	// Fields from properties (exact original casing from metadata)
	for _, p := range props {
		fieldKey := p.Name
		zodType := getZodType(p.Type, p.Nullable, "")
		fields.WriteString(fmt.Sprintf("\t%s: %s,\n", fieldKey, zodType))
	}

	// Navigation properties (exact original casing from metadata)
	for _, n := range navs {
		fieldKey := n.Name
		targetName := extractEdmTypeName(n.Type)
		targetSchema := strings.Title(targetName) + "Schema"
		isColl, _ := isCollection(n.Type)
		var zodType string
		if isColl {
			zodType = fmt.Sprintf("z.array(z.lazy(() => %s))", targetSchema)
			zodType += ".nullish()"
		} else {
			zodType = fmt.Sprintf("z.lazy(() => %s)", targetSchema)
			zodType += ".nullish()"
		}
		fields.WriteString(fmt.Sprintf("\t%s: %s,\n", fieldKey, zodType))
	}

	fields.WriteString("});\n")
	fields.WriteString(fmt.Sprintf("export type %s = z.infer<typeof %s>;\n\n", tsTypeName, schemaName))
	return fields.String()
}

// Helper to convert enum member name to SAP B1 JSON string casing (lowercase first letter).
func toSapJsonEnumValue(memberName string) string {
	if len(memberName) == 0 {
		return memberName
	}
	return strings.ToLower(memberName[0:1]) + memberName[1:]
}

// Generate TS enum + Zod schema for EnumType, handling SAP B1 string casing in JSON.
func generateZodEnum(e EnumType) string {
	var members strings.Builder
	members.WriteString(fmt.Sprintf("export const %s = {\n", e.Name))

	currentValue := 0
	for _, m := range e.Members {
		var valStr string
		jsonValue := toSapJsonEnumValue(m.Name)
		if m.Value != "" {
			val, err := strconv.Atoi(m.Value)
			if err != nil {
				valStr = fmt.Sprintf("%d /* parsed error: %s */", currentValue, m.Value)
			} else {
				valStr = fmt.Sprintf("%d", val)
				currentValue = val + 1
			}
		} else {
			valStr = fmt.Sprintf("%d", currentValue)
			currentValue++
		}
		memberName := strings.Title(m.Name) // PascalCase for TS key
		members.WriteString(fmt.Sprintf("\t%s: '%s', // numeric value: %s\n", memberName, jsonValue, valStr))
	}
	members.WriteString("} as const;\n\n")

	// Type from const
	members.WriteString(fmt.Sprintf("export type %s = typeof %s[keyof typeof %s];\n\n", e.Name, e.Name, e.Name))

	// Zod schema using z.enum with the JSON string values
	schemaName := e.Name + "Schema"
	members.WriteString(fmt.Sprintf("export const %s = z.enum(Object.values(%s));\n", schemaName, e.Name))
	members.WriteString(fmt.Sprintf("export type %sType = z.infer<typeof %s>;\n\n", e.Name, schemaName))
	return members.String()
}

// Debug function to dump parsed structure as XML.
func dumpParsedXML(edmx *EDMX, filename string) {
	data, err := xml.MarshalIndent(edmx, "", "  ")
	if err != nil {
		log.Printf("Error dumping parsed structure: %v", err)
		return
	}
	_ = ioutil.WriteFile(filename, data, 0644)
	log.Printf("Dumped parsed structure to %s for debugging", filename)
}

// ---------- Splitting helpers ----------

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

func writeFile(path string, content string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func toSortedSlice(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func makeSet(values []string) map[string]struct{} {
	s := make(map[string]struct{}, len(values))
	for _, v := range values {
		s[v] = struct{}{}
	}
	return s
}

func collectTypeAndEnumDeps(
	typ interface{},
	entitySet map[string]struct{},
	complexSet map[string]struct{},
	enumSet map[string]struct{},
) (typeDeps map[string]struct{}, enumDeps map[string]struct{}) {
	typeDeps = map[string]struct{}{}
	enumDeps = map[string]struct{}{}

	var props []Property
	var navs []NavigationProperty
	var selfName string

	switch t := typ.(type) {
	case EntityType:
		selfName = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	case ComplexType:
		selfName = t.Name
		props = t.Properties
		navs = t.NavigationProperties
	}

	addType := func(name string) {
		if name == "" || name == selfName {
			return
		}
		if _, isPrimitive := edmToZod[name]; isPrimitive {
			return
		}
		if _, isEnum := enumSet[name]; isEnum {
			enumDeps[name] = struct{}{}
			return
		}
		// else it's entity or complex
		if _, ok := entitySet[name]; ok {
			typeDeps[name] = struct{}{}
			return
		}
		if _, ok := complexSet[name]; ok {
			typeDeps[name] = struct{}{}
			return
		}
		// unknown type name, still add as type dep to be safe
		typeDeps[name] = struct{}{}
	}

	// scalar properties
	for _, p := range props {
		_, inner := isCollection(p.Type)
		innerName := extractEdmTypeName(inner)
		addType(innerName)
	}

	// navigation properties always point to entity or collection of entity
	for _, n := range navs {
		_, inner := isCollection(n.Type)
		innerName := extractEdmTypeName(inner)
		addType(innerName)
	}

	return
}

func renderPerTypeFile(
	typ interface{},
	isEntity bool,
	entitySet map[string]struct{},
	complexSet map[string]struct{},
	enumSet map[string]struct{},
	generatedAt string,
) (fileName string, content string) {
	var name string
	switch t := typ.(type) {
	case EntityType:
		name = t.Name
	case ComplexType:
		name = t.Name
	}
	titleName := strings.Title(name)
	fileName = titleName + ".ts"

	// Collect dependencies
	typeDeps, enumDeps := collectTypeAndEnumDeps(typ, entitySet, complexSet, enumSet)
	typeDepNames := toSortedSlice(typeDeps)
	enumDepNames := toSortedSlice(enumDeps)

	// Build imports
	var b strings.Builder

	b.WriteString("// Generated from OData EDMX for SAP Business One Service Layer v2\n")
	b.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
	b.WriteString(fmt.Sprintf("// Generated at %s\n\n", generatedAt))
	b.WriteString("import { z, ZodType } from 'zod';\n")

	// Enums: import type + schema in one module
	if len(enumDepNames) > 0 {
		// type imports
		b.WriteString(fmt.Sprintf("import type { %s } from '../enums';\n",
			strings.Join(enumDepNames, ", ")))
		// value imports (schemas)
		enumSchemas := make([]string, 0, len(enumDepNames))
		for _, e := range enumDepNames {
			enumSchemas = append(enumSchemas, e+"Schema")
		}
		b.WriteString(fmt.Sprintf("import { %s } from '../enums';\n", strings.Join(enumSchemas, ", ")))
	}

	// Type deps: import type and schema from correct folders
	for _, dep := range typeDepNames {
		depPath := ""
		// decide folder and relative path
		if _, ok := entitySet[dep]; ok {
			if isEntity {
				depPath = "./" + strings.Title(dep)
			} else {
				depPath = "../entities/" + strings.Title(dep)
			}
		} else if _, ok := complexSet[dep]; ok {
			if isEntity {
				depPath = "../complex/" + strings.Title(dep)
			} else {
				depPath = "./" + strings.Title(dep)
			}
		} else {
			// fallback assume entity in sibling (better than nothing)
			if isEntity {
				depPath = "./" + strings.Title(dep)
			} else {
				depPath = "./" + strings.Title(dep)
			}
		}
		// type-only import for friendly type
		b.WriteString(fmt.Sprintf("import type { %s } from '%s';\n", strings.Title(dep), depPath))
		// schema import
		b.WriteString(fmt.Sprintf("import { %sSchema } from '%s';\n", strings.Title(dep), depPath))
	}

	if len(enumDepNames) > 0 || len(typeDepNames) > 0 {
		b.WriteString("\n")
	}

	// Model + Schema
	b.WriteString(generateTsModelType(typ))
	b.WriteString(generateZodSchema(typ, isEntity, ""))

	content = b.String()
	return
}

func writePerTypeOutputs(
	edmx *EDMX,
	outDir string,
) error {
	generatedAt := time.Now().Format(time.RFC3339)

	// Index sets for quick lookups
	enumSet := map[string]struct{}{}
	entitySet := map[string]struct{}{}
	complexSet := map[string]struct{}{}

	var allEnums []EnumType
	var allEntities []EntityType
	var allComplexes []ComplexType

	for _, schema := range edmx.DataServices.Schemas {
		allEnums = append(allEnums, schema.EnumTypes...)
		allEntities = append(allEntities, schema.EntityTypes...)
		allComplexes = append(allComplexes, schema.ComplexTypes...)
	}

	for _, e := range allEnums {
		enumSet[e.Name] = struct{}{}
	}
	for _, e := range allEntities {
		entitySet[e.Name] = struct{}{}
	}
	for _, c := range allComplexes {
		complexSet[c.Name] = struct{}{}
	}

	// 1) Write enums.ts
	{
		var b strings.Builder
		b.WriteString("// Generated enums from OData EDMX for SAP Business One Service Layer v2\n")
		b.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
		b.WriteString(fmt.Sprintf("// Generated at %s\n\n", generatedAt))
		b.WriteString("import { z } from 'zod';\n\n")

		for _, en := range allEnums {
			b.WriteString(generateZodEnum(en))
		}

		enumsPath := filepath.Join(outDir, "enums.ts")
		if err := writeFile(enumsPath, b.String()); err != nil {
			return fmt.Errorf("writing enums.ts: %w", err)
		}
		log.Printf("Wrote %s", enumsPath)
	}

	// 2) Write per-entity files
	entityDir := filepath.Join(outDir, "entities")
	if err := ensureDir(entityDir); err != nil {
		return err
	}
	entityNames := make([]string, 0, len(allEntities))
	for _, et := range allEntities {
		fileName, content := renderPerTypeFile(et, true, entitySet, complexSet, enumSet, generatedAt)
		target := filepath.Join(entityDir, fileName)
		if err := writeFile(target, content); err != nil {
			return fmt.Errorf("writing entity file %s: %w", target, err)
		}
		entityNames = append(entityNames, strings.TrimSuffix(fileName, ".ts"))
		log.Printf("Wrote %s", target)
	}

	// 3) Write per-complex files
	complexDir := filepath.Join(outDir, "complex")
	if err := ensureDir(complexDir); err != nil {
		return err
	}
	complexNames := make([]string, 0, len(allComplexes))
	for _, ct := range allComplexes {
		fileName, content := renderPerTypeFile(ct, false, entitySet, complexSet, enumSet, generatedAt)
		target := filepath.Join(complexDir, fileName)
		if err := writeFile(target, content); err != nil {
			return fmt.Errorf("writing complex file %s: %w", target, err)
		}
		complexNames = append(complexNames, strings.TrimSuffix(fileName, ".ts"))
		log.Printf("Wrote %s", target)
	}

	// 4) Barrel files
	// entities/index.ts
	{
		sort.Strings(entityNames)
		var b strings.Builder
		b.WriteString("// Barrel file for entity schemas\n")
		for _, n := range entityNames {
			b.WriteString(fmt.Sprintf("export * from './%s';\n", n))
		}
		if err := writeFile(filepath.Join(entityDir, "index.ts"), b.String()); err != nil {
			return err
		}
	}
	// complex/index.ts
	{
		sort.Strings(complexNames)
		var b strings.Builder
		b.WriteString("// Barrel file for complex schemas\n")
		for _, n := range complexNames {
			b.WriteString(fmt.Sprintf("export * from './%s';\n", n))
		}
		if err := writeFile(filepath.Join(complexDir, "index.ts"), b.String()); err != nil {
			return err
		}
	}
	// root index.ts
	{
		var b strings.Builder
		b.WriteString("// Root barrel file\n")
		b.WriteString("export * from './enums';\n")
		b.WriteString("export * from './entities';\n")
		b.WriteString("export * from './complex';\n")
		if err := writeFile(filepath.Join(outDir, "index.ts"), b.String()); err != nil {
			return err
		}
	}

	return nil
}

// ---------- Main (supports single file or per-type split) ----------

func main() {
	inputFile := flag.String("input", "", "Path to the EDMX XML file")
	outputFile := flag.String("output", "types.ts", "Path to the output TS file for -split=single")
	outDir := flag.String("outDir", "types", "Directory to write TS files for -split=perType")
	splitMode := flag.String("split", "perType", "Output mode: single | perType")
	dumpParsed := flag.Bool("dump", false, "Dump parsed XML structure to debug.xml")
	flag.Parse()

	if *inputFile == "" {
		log.Fatal("Please provide -input flag with the XML file path")
	}

	data, err := ioutil.ReadFile(*inputFile)
	if err != nil {
		log.Fatalf("Error reading XML file: %v", err)
	}

	var edmx EDMX
	if err := xml.Unmarshal(data, &edmx); err != nil {
		log.Fatalf("Error unmarshaling XML: %v", err)
	}

	log.Printf("Parsed EDMX Version: %s", edmx.Version)
	log.Printf("Parsed %d schemas", len(edmx.DataServices.Schemas))

	if *dumpParsed {
		dumpParsedXML(&edmx, "debug.xml")
	}

	switch *splitMode {
	case "single":
		var output strings.Builder
		output.WriteString("// Generated Zod schemas from OData EDMX for SAP Business One Service Layer v2\n")
		output.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
		output.WriteString(fmt.Sprintf("// Generated at %s\n\n", time.Now().Format(time.RFC3339)))

		// TS imports
		output.WriteString("import { z, ZodType } from 'zod';\n\n")

		// First, collect and generate ALL enums from ALL schemas to minimize forward refs
		allEnums := make(map[string]string)
		for i, schema := range edmx.DataServices.Schemas {
			for _, en := range schema.EnumTypes {
				enumCode := generateZodEnum(en)
				allEnums[en.Name] = enumCode
				log.Printf("  Pre-generated EnumType Schema: %s from schema %d", en.Name, i+1)
			}
		}

		// Output all enums first
		for _, enumCode := range allEnums {
			output.WriteString(enumCode)
		}

		// Generate TS model types (NameModel) for all entities/complex first
		var allModelTypes strings.Builder
		for _, schema := range edmx.DataServices.Schemas {
			for _, et := range schema.EntityTypes {
				allModelTypes.WriteString(generateTsModelType(et))
			}
			for _, ct := range schema.ComplexTypes {
				allModelTypes.WriteString(generateTsModelType(ct))
			}
		}
		output.WriteString(allModelTypes.String())

		// Now generate Zod object schemas (entities/complex) for all schemas
		generatedCount := len(allEnums)
		for i, schema := range edmx.DataServices.Schemas {
			log.Printf("Processing schema %d: %s (Alias: %s)", i+1, schema.Namespace, schema.Alias)
			log.Printf("  - %d EntityTypes", len(schema.EntityTypes))
			log.Printf("  - %d ComplexTypes", len(schema.ComplexTypes))

			for _, et := range schema.EntityTypes {
				output.WriteString(generateZodSchema(et, true, schema.Namespace))
				generatedCount++
				log.Printf("  Generated EntityType Schema: %s", et.Name)
			}
			for _, ct := range schema.ComplexTypes {
				output.WriteString(generateZodSchema(ct, false, schema.Namespace))
				generatedCount++
				log.Printf("  Generated ComplexType Schema: %s", ct.Name)
			}
		}

		if generatedCount == 0 {
			log.Println("Warning: No types generated.")
			output.WriteString("// No schemas found in metadata.\n")
		} else {
			log.Printf("Successfully generated %d schemas (including %d enums)", generatedCount, len(allEnums))
		}

		err = ioutil.WriteFile(*outputFile, []byte(output.String()), 0644)
		if err != nil {
			log.Fatalf("Error writing output file: %v", err)
		}
		log.Printf("Generated Zod schemas in %s", *outputFile)

	case "perType":
		if err := writePerTypeOutputs(&edmx, *outDir); err != nil {
			log.Fatalf("Error generating per-type outputs: %v", err)
		}
		log.Printf("Generated per-type TS files in %s", *outDir)

	default:
		log.Fatalf("Unknown -split mode: %s (use 'single' or 'perType')", *splitMode)
	}
}
