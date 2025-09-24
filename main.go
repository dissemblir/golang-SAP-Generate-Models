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
	"strings"
	"time"
)

/*
Generator: ArkType-only output (no Zod, no TS inference export)

- Emits ArkType validators with `type({ ... })` or type("...'a'|'b'...")
- Each property is optional and allows null: "FieldName?": "<base>|null"
- Collections: "<base>[]|null"
- Enums: "'A'|'B'|...'"; used directly in property DSL when referenced
- Navigation properties: shallow "object|null" or "object[]|null" (no cross-file linking)
- No `export type Foo = Infer<...>` lines (you said you'll infer yourself)
- No index/barrel files (prevents pulling everything into the editor at once)

SPECIAL CASE (SAP B1 quirk):
- If a property name ends with "Property" (e.g., "ActivityProperty") and there is
  no sibling property with the alias name (e.g., "Activity"), we emit the alias key
  instead, e.g. "Activity?" in the ArkType shape. This matches actual JSON payloads.

Usage:
  Split per type (recommended):
    go run main.go -input="metadata.xml" -outDir="./types" -split="perType"
  Single file (legacy):
    go run main.go -input="metadata.xml" -output="types.ts" -split="single"
*/

// ========================= EDMX parsing types =========================

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

// ========================= Type mappings =========================

var edmToArkBase = map[string]string{
	"String":         "string",
	"Int16":          "number",
	"Int32":          "number",
	"Int64":          "number",
	"Byte":           "number",
	"SByte":          "number",
	"Boolean":        "boolean",
	"Decimal":        "number", // change to "string" if your API sends decimals as strings
	"Double":         "number",
	"Single":         "number",
	"Guid":           "string",
	"Date":           "string", // ISO date string
	"DateTimeOffset": "string", // ISO date-time string
	"TimeOfDay":      "string", // "HH:MM:SS"
	"Binary":         "string", // base64
	"Stream":         "string",
	"Duration":       "string",
}

// ========================= Helpers =========================

func extractEdmTypeName(edmType string) string {
	fullType := strings.TrimPrefix(edmType, "Collection(")
	fullType = strings.TrimSuffix(fullType, ")")
	split := strings.SplitN(fullType, ".", 2)
	if len(split) == 2 {
		return split[1]
	}
	return fullType
}

func isCollection(t string) (bool, string) {
	if strings.HasPrefix(t, "Collection(") && strings.HasSuffix(t, ")") {
		return true, t[11 : len(t)-1]
	}
	return false, t
}

// Build ArkType DSL for a property: always optional key (handled by "Field?")
// and allow null in the value. Arrays become "<base>[]|null".
func arkPropTypeDSL(edmType string, isEnum bool, enumVals []string) string {
	isColl, inner := isCollection(edmType)
	innerName := extractEdmTypeName(inner)

	// Enum literal union
	if isEnum && len(enumVals) > 0 {
		var parts []string
		for _, v := range enumVals {
			parts = append(parts, fmt.Sprintf("'%s'", v))
		}
		union := strings.Join(parts, "|")
		if isColl {
			return union + "[]|null"
		}
		return union + "|null"
	}

	// Primitive
	if base, ok := edmToArkBase[innerName]; ok {
		if isColl {
			return base + "[]|null"
		}
		return base + "|null"
	}

	// Non-primitive -> shallow
	if isColl {
		return "object[]|null"
	}
	return "object|null"
}

// ========================= ArkType emission =========================

func generateArkEnum(e EnumType) string {
	// Unique member names (as SAP usually returns them, e.g., "cn_Meeting")
	seen := map[string]bool{}
	vals := []string{}
	for _, m := range e.Members {
		if !seen[m.Name] {
			seen[m.Name] = true
			vals = append(vals, m.Name)
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("export const %sType = type(\"", e.Name))
	for i, v := range vals {
		if i > 0 {
			b.WriteString("|")
		}
		b.WriteString(fmt.Sprintf("'%s'", v))
	}
	b.WriteString("\");\n\n")
	return b.String()
}

// Generate ArkType object. Applies "Property" aliasing:
// If a scalar property ends with "Property" and the alias (without suffix) does not
// exist as a sibling, we emit the alias key instead (matches actual JSON).
func generateArkObject(typ interface{}, enumsByName map[string][]string) string {
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

	typeName := strings.Title(name)
	var b strings.Builder
	b.WriteString(fmt.Sprintf("export const %sType = type({\n", typeName))

	// Build set of existing property names to avoid alias collisions
	propNames := make(map[string]struct{}, len(props))
	for _, p := range props {
		propNames[p.Name] = struct{}{}
	}

	// Scalar props
	for _, p := range props {
		innerName := extractEdmTypeName(p.Type)
		enumVals, isEnum := enumsByName[innerName]
		dsl := arkPropTypeDSL(p.Type, isEnum, enumVals)

		// Alias rule: if ends with "Property" and alias key doesn't exist, use alias
		keyName := p.Name
		if strings.HasSuffix(keyName, "Property") {
			alias := strings.TrimSuffix(keyName, "Property")
			if alias != "" {
				if _, exists := propNames[alias]; !exists {
					keyName = alias
				}
			}
		}

		// IMPORTANT: quoted key with ? for optional
		b.WriteString(fmt.Sprintf("  \"%s?\": \"%s\",\n", keyName, dsl))
	}

	// Navigation props (shallow) — we do not alias these
	for _, n := range navs {
		dsl := arkPropTypeDSL(n.Type, false, nil)
		b.WriteString(fmt.Sprintf("  \"%s?\": \"%s\",\n", n.Name, dsl))
	}

	b.WriteString("});\n\n")
	return b.String()
}

// ========================= I/O helpers =========================

func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0755)
}

func writeFile(path string, content string) error {
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

// ========================= Writers =========================

func writePerTypeOutputs(edmx *EDMX, outDir string) error {
	generatedAt := time.Now().Format(time.RFC3339)

	// Map enum name -> values for quick lookup
	enumsByName := map[string][]string{}
	for _, schema := range edmx.DataServices.Schemas {
		for _, en := range schema.EnumTypes {
			seen := map[string]bool{}
			vals := []string{}
			for _, m := range en.Members {
				if !seen[m.Name] {
					seen[m.Name] = true
					vals = append(vals, m.Name)
				}
			}
			enumsByName[en.Name] = vals
		}
	}

	// enums.ts
	{
		var b strings.Builder
		b.WriteString("// Generated ArkType enums from OData EDMX for SAP Business One Service Layer v2\n")
		b.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
		b.WriteString(fmt.Sprintf("// Generated at %s\n\n", generatedAt))
		b.WriteString(`import { type } from "arktype";` + "\n\n")

		// stable order across schemas
		var allEnums []EnumType
		for _, schema := range edmx.DataServices.Schemas {
			allEnums = append(allEnums, schema.EnumTypes...)
		}
		// sort by name for deterministic output
		sort.Slice(allEnums, func(i, j int) bool { return allEnums[i].Name < allEnums[j].Name })

		for _, en := range allEnums {
			b.WriteString(generateArkEnum(en))
		}

		enumsPath := filepath.Join(outDir, "enums.ts")
		if err := writeFile(enumsPath, b.String()); err != nil {
			return fmt.Errorf("writing enums.ts: %w", err)
		}
		log.Printf("Wrote %s", enumsPath)
	}

	// entities
	entityDir := filepath.Join(outDir, "entities")
	if err := ensureDir(entityDir); err != nil {
		return err
	}
	for _, schema := range edmx.DataServices.Schemas {
		for _, et := range schema.EntityTypes {
			var b strings.Builder
			b.WriteString("// Generated ArkType entity from OData EDMX for SAP Business One Service Layer v2\n")
			b.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
			b.WriteString(fmt.Sprintf("// Generated at %s\n\n", generatedAt))
			b.WriteString(`import { type } from "arktype";` + "\n\n")
			b.WriteString(generateArkObject(et, enumsByName))

			target := filepath.Join(entityDir, strings.Title(et.Name)+".ts")
			if err := writeFile(target, b.String()); err != nil {
				return fmt.Errorf("writing entity file %s: %w", target, err)
			}
			log.Printf("Wrote %s", target)
		}
	}

	// complex
	complexDir := filepath.Join(outDir, "complex")
	if err := ensureDir(complexDir); err != nil {
		return err
	}
	for _, schema := range edmx.DataServices.Schemas {
		for _, ct := range schema.ComplexTypes {
			var b strings.Builder
			b.WriteString("// Generated ArkType complex type from OData EDMX for SAP Business One Service Layer v2\n")
			b.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
			b.WriteString(fmt.Sprintf("// Generated at %s\n\n", generatedAt))
			b.WriteString(`import { type } from "arktype";` + "\n\n")
			b.WriteString(generateArkObject(ct, enumsByName))

			target := filepath.Join(complexDir, strings.Title(ct.Name)+".ts")
			if err := writeFile(target, b.String()); err != nil {
				return fmt.Errorf("writing complex file %s: %w", target, err)
			}
			log.Printf("Wrote %s", target)
		}
	}

	// No barrels to avoid loading everything at once
	return nil
}

func writeSingleFile(edmx *EDMX, outputFile string) error {
	var out strings.Builder
	out.WriteString("// Generated ArkType types from OData EDMX for SAP Business One Service Layer v2\n")
	out.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
	out.WriteString(fmt.Sprintf("// Generated at %s\n\n", time.Now().Format(time.RFC3339)))
	out.WriteString(`import { type } from "arktype";` + "\n\n")

	// enums map for props
	enumsByName := map[string][]string{}
	for _, schema := range edmx.DataServices.Schemas {
		for _, en := range schema.EnumTypes {
			seen := map[string]bool{}
			vals := []string{}
			for _, m := range en.Members {
				if !seen[m.Name] {
					seen[m.Name] = true
					vals = append(vals, m.Name)
				}
			}
			enumsByName[en.Name] = vals
		}
	}

	// Enums
	for _, schema := range edmx.DataServices.Schemas {
		for _, en := range schema.EnumTypes {
			out.WriteString(generateArkEnum(en))
		}
	}

	// Entities and Complex
	for _, schema := range edmx.DataServices.Schemas {
		for _, et := range schema.EntityTypes {
			out.WriteString(generateArkObject(et, enumsByName))
		}
		for _, ct := range schema.ComplexTypes {
			out.WriteString(generateArkObject(ct, enumsByName))
		}
	}

	return ioutil.WriteFile(outputFile, []byte(out.String()), 0644)
}

// ========================= main =========================

func main() {
	inputFile := flag.String("input", "", "Path to the EDMX XML file")
	outputFile := flag.String("output", "types.ts", "Path to the output TS file for -split=single")
	outDir := flag.String("outDir", "types", "Directory to write TS files for -split=perType")
	splitMode := flag.String("split", "perType", "Output mode: single | perType")
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

	switch *splitMode {
	case "single":
		if err := writeSingleFile(&edmx, *outputFile); err != nil {
			log.Fatalf("Error writing single output file: %v", err)
		}
		log.Printf("Generated ArkType types in %s", *outputFile)
	case "perType":
		if err := writePerTypeOutputs(&edmx, *outDir); err != nil {
			log.Fatalf("Error generating per-type outputs: %v", err)
		}
		log.Printf("Generated per-type ArkType TS files in %s", *outDir)
	default:
		log.Fatalf("Unknown -split mode: %s (use 'single' or 'perType')", *splitMode)
	}
}
