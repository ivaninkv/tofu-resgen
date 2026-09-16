package tfgen

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
)

// ReadmeOptions control generation of a module README.md.
type ReadmeOptions struct {
	// VarName is the module variable name, e.g. "vpc_subnet".
	VarName string
	// ResourceName is the provider resource type, e.g. "yandex_vpc_subnet".
	ResourceName string
	// ProviderAddr is the provider address, e.g. "yandex-cloud/yandex". It is
	// named only where the schema carries no resource description.
	ProviderAddr string
	// Description overrides the resource description from the schema.
	Description string
	// Doc emits schema descriptions as a column of the variable tables.
	Doc bool
	// Optional emits the optional attributes of the usage example as
	// commented lines carrying the values the module applies without them.
	// When false the example passes the required attributes only.
	Optional bool
	// Defaults supplies the value an optional attribute falls back to. May be nil.
	Defaults *Defaults
}

// sourcePlaceholder is the module source of the usage example. Neither the
// schema nor the module directory states where the module will be published,
// so the example carries placeholders the author replaces. The tool never
// inspects git.
const sourcePlaceholder = "git::<repo-url>//<path-to-module>?ref=<ref>"

// GenerateReadme renders README.md for one module.
//
// The file answers three questions and stops there: what the module creates,
// how to call it, and what the module variable holds. Every statement is read
// off the schema or the module options; nothing about the resource is
// invented, which is why the usage example carries a placeholder source and
// why optional attributes are documented rather than demonstrated.
func GenerateReadme(res schema.Schema, opt ReadmeOptions) ([]byte, error) {
	if opt.VarName == "" {
		return nil, errors.New("variable name is required")
	}
	if opt.ResourceName == "" {
		return nil, errors.New("resource name is required")
	}
	usage, err := readmeUsage(res, opt)
	if err != nil {
		return nil, err
	}
	g := readmeGen{
		r:       &Renderer{Defaults: opt.Defaults, Doc: opt.Doc},
		varName: opt.VarName,
		doc:     opt.Doc,
	}
	tables, err := g.level(nil, nil, res.ConfigAttributes())
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Модуль %s\n\n", opt.ResourceName)
	b.WriteString("## Описание\n\n")
	b.WriteString(readmeSummary(res, opt))
	b.WriteString("\n## Использование\n\n")
	b.WriteString("Замените `<repo-url>`, `<path-to-module>` и `<ref>` на адрес репозитория, путь к каталогу модуля и тег; `?ref=` можно убрать, если модуль берётся из ветки по умолчанию. ")
	if opt.Optional {
		b.WriteString("Обязательные параметры переданы активными строками, optional-атрибуты — закомментированными, со значениями, которые модуль применяет без них; заглушки `<...>` заменяются на реальные значения.\n")
	} else {
		b.WriteString("Передаются только обязательные параметры — остальные атрибуты переменная объявляет со значением по умолчанию.\n")
	}
	if !hasRequired(res.ConfigAttributes()) {
		if opt.Optional {
			b.WriteString("Обязательных атрибутов у ресурса нет: все строки примера закомментированы.\n")
		} else {
			b.WriteString("Обязательных атрибутов у ресурса нет, поэтому пример показывает пустой инстанс.\n")
		}
	}
	b.WriteString("\n```hcl\n")
	b.WriteString(usage)
	b.WriteString("```\n")
	fmt.Fprintf(&b, "\n## Переменная %s\n\n", tick(opt.VarName))
	b.WriteString("Тип — `map(object)`, по умолчанию `{}`: один элемент отображения (имя инстанса → конфигурация) создаёт один ресурс, модуль без элементов не создаёт ничего.\n")
	fmt.Fprintf(&b, "Колонка «Обяз.» — обязательность атрибута внутри своего объекта; у вложенных таблиц она действует при условии, что содержащий объект задан.\n\n")
	b.WriteString(tables)
	return []byte(b.String()), nil
}

// readmeSummary renders the opening paragraphs: what the module creates, and
// what the caller is expected to finish by hand.
func readmeSummary(res schema.Schema, opt ReadmeOptions) string {
	desc := strings.TrimSpace(opt.Description)
	if desc == "" {
		desc = strings.TrimSpace(res.Block.Description)
	}
	var b strings.Builder
	if desc != "" {
		b.WriteString(demoteHeadings(desc))
		b.WriteString("\n")
	} else {
		fmt.Fprintf(&b, "Создаёт ресурс %s провайдера %s.\n", tick(opt.ResourceName), tick(opt.ProviderAddr))
	}
	fmt.Fprintf(&b, "\nОдин элемент переменной %s (map: имя инстанса → конфигурация) — один ресурс %s.\n",
		tick(opt.VarName), tick(opt.ResourceName))
	b.WriteString("`main.tf` — скелет: атрибуты подключены к переменной; проводка `data`-источников и всё, что схема не описывает, дописывается вручную.\n")
	return b.String()
}

// readmeUsage renders the module block a user copies: the placeholder source
// and the example assignment, which carries the required attributes only.
func readmeUsage(res schema.Schema, opt ReadmeOptions) (string, error) {
	assignment, err := exampleAssignment(res, TFVarsOptions{
		VarName:      opt.VarName,
		ResourceName: opt.ResourceName,
		Optional:     opt.Optional,
		Defaults:     opt.Defaults,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "module %s {\n", strconv.Quote(opt.VarName))
	fmt.Fprintf(&b, "  source = %s\n\n", strconv.Quote(sourcePlaceholder))
	b.WriteString(assignment)
	b.WriteString("}\n")
	return string(hclwrite.Format([]byte(b.String()))), nil
}

// readmeGen renders the variable tables while walking the schema. A variable
// of a provider resource nests deeply enough (the fixture reaches seven
// levels) that one flat table would carry paths wider than the terminal, so
// every object level gets its own table, linked from the row that opens it.
type readmeGen struct {
	r       *Renderer
	varName string
	doc     bool
}

// level renders the table of one object level. path is the attribute path from
// the variable root, which is what the defaults file and the type renderer key
// on; seg is the same path with a collection segment per level, which is what
// terraform.tfvars.example placeholders spell out. Nested objects follow as
// their own sections, so that a level stays readable whatever it contains.
func (g *readmeGen) level(path, seg []string, attrs map[string]schema.Attribute) (string, error) {
	names := settable(attrs)
	if len(names) == 0 {
		// An object with nothing to set: the type says so, a table would only
		// carry a header.
		return "", nil
	}
	var table, nested strings.Builder
	g.tableHeader(&table)
	for _, name := range names {
		a := attrs[name]
		full := withSeg(path, name)
		label := tick(name)
		if a.NestedType != nil {
			child := withSeg(seg, name)
			if s, ok := collectionSegment(a); ok {
				child = withSeg(child, s)
			}
			label = fmt.Sprintf("[%s](#%s)", tick(name), anchor(withSeg([]string{g.varName}, child...)))
			section, err := g.section(full, child, a)
			if err != nil {
				return "", err
			}
			nested.WriteString(section)
		}
		typ, err := g.typeCell(full, a)
		if err != nil {
			return "", err
		}
		def, err := g.defaultCell(full)
		if err != nil {
			return "", err
		}
		row := []string{label, tick(typ), requirement(a), def}
		if g.doc {
			row = append(row, descriptionCell(a.Description))
		}
		fmt.Fprintf(&table, "| %s |\n", strings.Join(row, " | "))
	}
	return table.String() + nested.String(), nil
}

// section renders one nested object: its heading, its type and its own table.
func (g *readmeGen) section(path, seg []string, a schema.Attribute) (string, error) {
	typ, err := readmeType(a)
	if err != nil {
		return "", err
	}
	body, err := g.level(path, seg, a.NestedType.Attributes)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n### %s\n\n%s\n\n", tick(strings.Join(withSeg([]string{g.varName}, seg...), ".")), tick(typ))
	b.WriteString(body)
	return b.String(), nil
}

// demoteHeadings pushes the ATX headings of a schema description two levels
// down (dropping any deeper than H6), so that a provider's own outline —
// "## Defined types" and the like — cannot be confused with the sections of
// this file, and cannot take the anchors of its tables.
func demoteHeadings(desc string) string {
	lines := strings.Split(desc, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		level := 0
		for level < len(line) && line[level] == '#' {
			level++
		}
		if level == len(line) || line[level] != ' ' {
			continue // a line of hashes, not a heading
		}
		demoted := min(level+2, 6)
		lines[i] = strings.Repeat("#", demoted) + line[level:]
	}
	return strings.Join(lines, "\n")
}

// tableHeader opens one markdown table.
func (g *readmeGen) tableHeader(b *strings.Builder) {
	if g.doc {
		b.WriteString("| Атрибут | Тип | Обяз. | По умолчанию | Описание |\n")
		b.WriteString("|---|---|---|---|---|\n")
		return
	}
	b.WriteString("| Атрибут | Тип | Обяз. | По умолчанию |\n")
	b.WriteString("|---|---|---|---|\n")
}

// typeCell renders one type for a table cell.
func (g *readmeGen) typeCell(path []string, a schema.Attribute) (string, error) {
	if a.NestedType != nil {
		return readmeType(a)
	}
	t, err := g.r.attributeType(path, a)
	if err != nil {
		return "", err
	}
	// A type expression may span lines (a legacy `type` carrying an object);
	// a cell cannot.
	return strings.Join(strings.Fields(t), " "), nil
}

// defaultCell renders the value an optional attribute falls back to, or an
// em dash when the schema and the defaults file declare none.
func (g *readmeGen) defaultCell(path []string) (string, error) {
	v, ok := g.r.Defaults.Lookup(path)
	if !ok {
		return "—", nil
	}
	lit, err := hclLiteral(v)
	if err != nil {
		return "", fmt.Errorf("default for %s: %w", joinPath(path...), err)
	}
	return tick(lit), nil
}

// readmeType renders the abbreviated type of a nested object: the table of the
// object itself carries its fields, so the cell names the collection and stops.
// An object with no field to set says so, exactly as variables.tf spells it.
func readmeType(a schema.Attribute) (string, error) {
	if a.NestedType == nil {
		return "", errors.New("attribute has no nested type")
	}
	elem := "object"
	if len(settable(a.NestedType.Attributes)) == 0 {
		elem = "object({})"
	}
	switch a.NestedType.NestingMode {
	case "single":
		return elem, nil
	case "list", "set", "map":
		return collectionOf(a.NestedType.NestingMode, elem)
	default:
		return "", fmt.Errorf("unsupported nesting mode %q", a.NestedType.NestingMode)
	}
}

// settable lists the attributes of a level a configuration may set, in
// lexical order.
func settable(attrs map[string]schema.Attribute) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		if !attrs[name].ComputedOnly() {
			out = append(out, name)
		}
	}
	return out
}

// requirement renders the mandatory column.
func requirement(a schema.Attribute) string {
	if a.Required {
		return "да"
	}
	return "нет"
}

// hasRequired reports whether a level holds a mandatory attribute. With none,
// a module call can only show an empty instance.
func hasRequired(attrs map[string]schema.Attribute) bool {
	for _, name := range settable(attrs) {
		if attrs[name].Required {
			return true
		}
	}
	return false
}

// collectionSegment is the path segment a collection adds to the placeholders
// of terraform.tfvars.example: one element per list/set, one key per map.
func collectionSegment(a schema.Attribute) (string, bool) {
	if a.NestedType == nil {
		return "", false
	}
	switch a.NestedType.NestingMode {
	case "list", "set":
		return "item", true
	case "map":
		return "key", true
	default:
		return "", false
	}
}

// descriptionCell normalises a schema description into one table cell. The
// descriptions are markdown and often span lines: whitespace is collapsed, and
// the characters that would break the cell are escaped.
func descriptionCell(desc string) string {
	s := strings.Join(strings.Fields(desc), " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "<", "&lt;")
}

// anchor renders the GitHub anchor of a heading spelled as a dotted path:
// lower case, punctuation dropped, spaces hyphenated.
func anchor(seg []string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.Join(seg, ".")) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// withSeg appends a segment to a path without sharing its backing array.
func withSeg(path []string, seg ...string) []string {
	out := make([]string, 0, len(path)+len(seg))
	out = append(out, path...)
	return append(out, seg...)
}

// tick wraps a value in backticks for markdown.
func tick(s string) string {
	return "`" + s + "`"
}
