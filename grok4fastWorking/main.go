package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"strconv"
	"strings"
	"time"
)

//Usage go run main.go -input="metadata.xml" -output="types.go"

// EDMX represents the root Edmx element.
type EDMX struct {
	XMLName      xml.Name     `xml:"http://docs.oasis-open.org/odata/ns/edmx Edmx"`
	Version      string       `xml:"Version,attr"`
	DataServices DataServices `xml:"http://docs.oasis-open.org/odata/ns/edmx DataServices"`
}

// DataServices holds the schemas (in edmx namespace).
type DataServices struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edmx DataServices"`
	Schemas []Schema `xml:"http://docs.oasis-open.org/odata/ns/edm Schema"`
}

// Schema represents a schema in the EDMX.
type Schema struct {
	XMLName          xml.Name          `xml:"http://docs.oasis-open.org/odata/ns/edm Schema"`
	Namespace        string            `xml:"Namespace,attr"`
	Alias            string            `xml:"Alias,attr,omitempty"`
	EntityTypes      []EntityType      `xml:"http://docs.oasis-open.org/odata/ns/edm EntityType"`
	ComplexTypes     []ComplexType     `xml:"http://docs.oasis-open.org/odata/ns/edm ComplexType"`
	EnumTypes        []EnumType        `xml:"http://docs.oasis-open.org/odata/ns/edm EnumType"`
	EntityContainers []EntityContainer `xml:"http://docs.oasis-open.org/odata/ns/edm EntityContainer,omitempty"`
	// Other elements like Annotations, etc., omitted for simplicity.
}

// EntityType holds the definition of an entity.
type EntityType struct {
	XMLName              xml.Name             `xml:"http://docs.oasis-open.org/odata/ns/edm EntityType"`
	Name                 string               `xml:"Name,attr"`
	Key                  []PropertyRef        `xml:"http://docs.oasis-open.org/odata/ns/edm Key>PropertyRef"`
	Properties           []Property           `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	NavigationProperties []NavigationProperty `xml:"http://docs.oasis-open.org/odata/ns/edm NavigationProperty"`
	// Base for inheritance, if needed.
	Base string `xml:"Base,attr,omitempty"`
}

// ComplexType similar to EntityType but without Key.
type ComplexType struct {
	XMLName              xml.Name             `xml:"http://docs.oasis-open.org/odata/ns/edm ComplexType"`
	Name                 string               `xml:"Name,attr"`
	Properties           []Property           `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	NavigationProperties []NavigationProperty `xml:"http://docs.oasis-open.org/odata/ns/edm NavigationProperty"`
}

// EnumType for enums.
type EnumType struct {
	XMLName        xml.Name     `xml:"http://docs.oasis-open.org/odata/ns/edm EnumType"`
	Name           string       `xml:"Name,attr"`
	Flags          bool         `xml:"Flags,attr,omitempty"`
	IsFlags        bool         `xml:"IsFlags,attr,omitempty"` // Alternative attr
	UnderlyingType string       `xml:"UnderlyingType,attr,omitempty"`
	Members        []EnumMember `xml:"http://docs.oasis-open.org/odata/ns/edm Member"`
}

type EnumMember struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm Member"`
	Name    string   `xml:"Name,attr"`
	Value   string   `xml:"Value,attr,omitempty"`
}

// Property represents a property.
type Property struct {
	XMLName      xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm Property"`
	Name         string   `xml:"Name,attr"`
	Type         string   `xml:"Type,attr"`
	Nullable     bool     `xml:"Nullable,attr,omitempty"`
	MaxLength    int      `xml:"MaxLength,attr,omitempty"`
	Precision    int      `xml:"Precision,attr,omitempty"`
	Scale        int      `xml:"Scale,attr,omitempty"`
	DefaultValue string   `xml:"DefaultValue,attr,omitempty"`
	// SAP-specific? Add if present.
}

// PropertyRef for keys.
type PropertyRef struct {
	XMLName xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm PropertyRef"`
	Name    string   `xml:"Name,attr"`
}

// NavigationProperty for relationships.
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

// EntityContainer for EntitySets, etc. (basic, for completeness).
type EntityContainer struct {
	XMLName    xml.Name    `xml:"http://docs.oasis-open.org/odata/ns/edm EntityContainer"`
	Name       string      `xml:"Name,attr"`
	EntitySets []EntitySet `xml:"http://docs.oasis-open.org/odata/ns/edm EntitySet,omitempty"`
	// Singleton, FunctionImport, etc., omitted.
}

type EntitySet struct {
	XMLName    xml.Name `xml:"http://docs.oasis-open.org/odata/ns/edm EntitySet"`
	Name       string   `xml:"Name,attr"`
	EntityType string   `xml:"EntityType,attr"`
}

// Type mappings from EDM primitive types to Go (keys without "Edm." prefix).
var edmToGo = map[string]string{
	"String":         "string",
	"Int16":          "int16",
	"Int32":          "int32",
	"Int64":          "int64",
	"Byte":           "uint8",
	"SByte":          "int8",
	"Boolean":        "bool",
	"Decimal":        "float64", // Changed to float64 for simplicity; use *big.Decimal if needed
	"Double":         "float64",
	"Single":         "float32",
	"Guid":           "string",
	"Date":           "time.Time",
	"DateTimeOffset": "time.Time",
	"TimeOfDay":      "time.Time",
	"Binary":         "[]byte",
	"Stream":         "[]byte",
	"Duration":       "string", // Or custom type
}

// Helper to extract the type name without namespace (e.g., "CAG_NS.BOE_IntDocNumber" -> "BOE_IntDocNumber", "Edm.Int32" -> "Int32").
func extractEdmTypeName(edmType string) string {
	// Handle Collection wrapper first.
	fullType := strings.TrimPrefix(edmType, "Collection(")
	fullType = strings.TrimSuffix(fullType, ")")
	split := strings.SplitN(fullType, ".", 2)
	if len(split) == 2 {
		return split[1] // Local name, e.g., "Int32" or "SalesOrder"
	}
	return fullType // Fallback
}

// Helper to determine if a type is collection and get inner type.
func isCollection(t string) (bool, string) {
	if strings.HasPrefix(t, "Collection(") && strings.HasSuffix(t, ")") {
		inner := t[11 : len(t)-1]
		return true, inner
	}
	return false, t
}

// Get Go type for a given EDM type.
func getGoType(edmType string, isNullable bool) string {
	isColl, innerEdm := isCollection(edmType)
	innerName := extractEdmTypeName(innerEdm)

	var baseGoType string
	if primitive, ok := edmToGo[innerName]; ok {
		baseGoType = primitive
	} else {
		// Non-primitive: use the local name (e.g., "BOE_SalesOrder")
		baseGoType = innerName
		if !isColl && isNullable {
			baseGoType = "*" + baseGoType // Pointer for optional complex types
		}
	}

	if isColl {
		baseGoType = "[]" + baseGoType
		return baseGoType // Collections are inherently nullable
	}

	// For non-collection primitives, apply nullability.
	if isNullable {
		switch baseGoType {
		case "string", "[]byte", "time.Time": // These can be zero/empty
			// No pointer needed
		default:
			if !strings.HasPrefix(baseGoType, "*") {
				baseGoType = "*" + baseGoType
			}
		}
	}

	return baseGoType
}

// Generate struct for EntityType or ComplexType.
func generateStruct(typ interface{}, isEntity bool, schemaNs string) string {
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

	var fields strings.Builder
	fields.WriteString(fmt.Sprintf("type %s struct {\n", name))

	// Fields from properties
	for _, p := range props {
		fieldName := strings.Title(p.Name) // CamelCase
		goType := getGoType(p.Type, p.Nullable)
		jsonTag := fmt.Sprintf("json:\"%s\"", p.Name)
		if p.Nullable {
			jsonTag += ",omitempty"
		}
		fields.WriteString(fmt.Sprintf("\t%s %s `%s`\n", fieldName, goType, jsonTag))
	}

	// Navigation properties
	for _, n := range navs {
		fieldName := strings.Title(n.Name)
		isColl, innerEdm := isCollection(n.Type)
		innerName := extractEdmTypeName(innerEdm)
		var goType string
		if primitive, ok := edmToGo[innerName]; ok {
			goType = primitive
		} else {
			goType = innerName // Target entity/complex type
		}

		// Determine multiplicity: to-one (non-Collection) -> *Type, to-many (Collection) -> []Type
		var jsonTag string
		if isColl {
			goType = "[]" + goType
			jsonTag = fmt.Sprintf("json:\"%s,omitempty\"", n.Name)
		} else {
			goType = "*" + goType // Pointer for optional to-one
			jsonTag = fmt.Sprintf("json:\"%s,omitempty\"", n.Name)
		}
		fields.WriteString(fmt.Sprintf("\t%s %s `%s`\n", fieldName, goType, jsonTag))
	}

	fields.WriteString("}\n\n")
	return fields.String()
}

// Handle enums as iota or const with values.
func generateEnum(e EnumType) string {
	var members strings.Builder
	members.WriteString(fmt.Sprintf("type %s int\n\n", e.Name))
	members.WriteString("const (\n")

	currentValue := 0
	for _, m := range e.Members {
		var valStr string
		if m.Value != "" {
			val, err := strconv.Atoi(m.Value)
			if err != nil {
				log.Printf("Warning: Invalid enum value '%s' for %s.%s, using %d: %v", m.Value, e.Name, m.Name, currentValue, err)
				valStr = fmt.Sprintf("%d", currentValue)
			} else {
				valStr = fmt.Sprintf("%d", val)
				currentValue = val + 1
			}
		} else {
			valStr = fmt.Sprintf("%d", currentValue)
			currentValue++
		}
		memberName := strings.Title(m.Name)
		members.WriteString(fmt.Sprintf("\t%s%s %s = %s\n", e.Name, memberName, e.Name, valStr))
	}
	members.WriteString(")\n\n")
	return members.String()
}

// Debug function to dump parsed structure as XML.
func dumpParsedXML(edmx *EDMX, filename string) {
	data, err := xml.MarshalIndent(edmx, "", "  ")
	if err != nil {
		log.Printf("Error dumping parsed XML: %v", err)
		return
	}
	ioutil.WriteFile(filename, data, 0644)
	log.Printf("Dumped parsed structure to %s for debugging", filename)
}

func main() {
	inputFile := flag.String("input", "", "Path to the EDMX XML file")
	outputFile := flag.String("output", "types.go", "Path to the output Go file")
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

	var output strings.Builder
	output.WriteString("// Generated types from OData EDMX for SAP Business One Service Layer v2\n")
	output.WriteString("// DO NOT EDIT - Regenerate from metadata.\n")
	output.WriteString(fmt.Sprintf("// Generated at %s\n\n", time.Now().Format(time.RFC3339)))

	// Package and imports
	output.WriteString("package odata\n\n") // Customize package name as needed
	output.WriteString("import (\n")
	output.WriteString("\t\"time\"\n")
	output.WriteString(")\n\n")

	// Generate for all schemas
	generatedCount := 0
	for i, schema := range edmx.DataServices.Schemas {
		log.Printf("Processing schema %d: %s (Alias: %s)", i+1, schema.Namespace, schema.Alias)
		log.Printf("  - %d EntityTypes", len(schema.EntityTypes))
		log.Printf("  - %d ComplexTypes", len(schema.ComplexTypes))
		log.Printf("  - %d EnumTypes", len(schema.EnumTypes))
		log.Printf("  - %d EntityContainers", len(schema.EntityContainers))

		for _, et := range schema.EntityTypes {
			output.WriteString(generateStruct(et, true, schema.Namespace))
			generatedCount++
			log.Printf("  Generated EntityType: %s", et.Name)
		}
		for _, ct := range schema.ComplexTypes {
			output.WriteString(generateStruct(ct, false, schema.Namespace))
			generatedCount++
			log.Printf("  Generated ComplexType: %s", ct.Name)
		}
		for _, en := range schema.EnumTypes {
			output.WriteString(generateEnum(en))
			generatedCount++
			log.Printf("  Generated EnumType: %s", en.Name)
		}
	}

	if generatedCount == 0 {
		log.Println("Warning: No types generated. This could indicate namespace mismatches or unusual XML structure.")
		log.Println("Tip: Run with -dump=true to generate 'debug.xml' and inspect the parsed structure.")
		log.Println("Common issues: Custom SAP namespaces, version differences, or annotations wrapping content.")
		output.WriteString("// No types found in metadata. Verify the EDMX file and consider -dump flag for debugging.\n")
	} else {
		log.Printf("Successfully generated %d types", generatedCount)
	}

	formatted, err := format.Source([]byte(output.String()))
	if err != nil {
		log.Printf("Warning: could not format output: %v", err)
		err = ioutil.WriteFile(*outputFile, []byte(output.String()), 0644)
	} else {
		err = ioutil.WriteFile(*outputFile, formatted, 0644)
	}
	if err != nil {
		log.Fatalf("Error writing output file: %v", err)
	}

	log.Printf("Generated file: %s", *outputFile)
}
