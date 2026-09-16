package tfgen

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/hashicorp/hcl/v2/hclparse"
)

// readmeOptions is the option set the CLI hands to GenerateReadme in -module
// mode.
func readmeOptions(name string) ReadmeOptions {
	return ReadmeOptions{
		VarName:      deriveTestVarName(name),
		ResourceName: name,
		ProviderAddr: fixtureProvider,
		Doc:          true,
	}
}

// TestReadmePinsTheModuleContract pins what a reader can check against the
// schema: what the module creates, that the call passes the required
// attributes only, and that the variable table reflects the resource.
func TestReadmePinsTheModuleContract(t *testing.T) {
	res := loadResource(t, "yandex_vpc_subnet")
	opt := readmeOptions("yandex_vpc_subnet")
	opt.Defaults = NewDefaults(map[string]any{"description": "managed by tofu-resgen"})

	readme, err := GenerateReadme(res, opt)
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	second, err := GenerateReadme(res, opt)
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	if !bytes.Equal(readme, second) {
		t.Fatal("generation is not deterministic")
	}
	src := string(readme)

	// The resource description the schema carries opens the file.
	if !strings.HasPrefix(src, "# Модуль yandex_vpc_subnet\n\n## Описание\n\nManages a subnet within the Yandex Cloud.") {
		t.Errorf("schema description does not open the README:\n%s", src)
	}

	// The usage block is valid HCL and names no optional attribute: a module
	// call takes the mandatory parameters, everything else is a default.
	usage := hclFence(t, src)
	if _, diags := hclparse.NewParser().ParseHCL([]byte(usage), "README.md"); diags.HasErrors() {
		t.Fatalf("usage block does not parse: %s\n%s", diags, usage)
	}
	flat := flatten(usage)
	for _, want := range []string{
		`module "vpc_subnet" {`,
		`source = "git::<repo-url>//<path-to-module>?ref=<ref>"`,
		`network_id = "<network_id>"`,
		`v4_cidr_blocks = ["<v4_cidr_blocks.item>"]`,
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("usage block missing %q:\n%s", want, usage)
		}
	}
	for _, unwanted := range []string{"zone", "labels", "dhcp_options", "description", "folder_id"} {
		if strings.Contains(flat, unwanted) {
			t.Errorf("usage block passes optional attribute %q:\n%s", unwanted, usage)
		}
	}

	// The table carries every settable attribute, the mode of each
	// collection, the module defaults, and no computed-only field.
	for _, want := range []string{
		"## Переменная `vpc_subnet`\n",
		"| Атрибут | Тип | Обяз. | По умолчанию | Описание |\n|---|---|---|---|---|\n",
		"| `description` | `string` | нет | `\"managed by tofu-resgen\"` | The resource description. |",
		"| `network_id` | `string` | да | — | ID of the network this subnet belongs to.",
		"| [`dhcp_options`](#vpc_subnetdhcp_optionsitem) | `list(object)` | нет | — | Options for DHCP client. |",
		"| [`timeouts`](#vpc_subnettimeouts) | `object` | нет | — |  |",
		"### `vpc_subnet.dhcp_options.item`\n\n`list(object)`\n",
		"| `domain_name_servers` | `list(string)` | нет | — | Domain name server IP addresses. |",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("README missing %q\n%s", want, src)
		}
	}
	for _, unwanted := range []string{"created_at", "subnet_ids", "v6_cidr_blocks"} {
		if strings.Contains(src, unwanted) {
			t.Errorf("computed-only attribute %q must not appear", unwanted)
		}
	}
}

// TestReadmeTablesEscapeDescriptions covers the two characters that would
// otherwise break a markdown cell: a pipe ends it, and an angle bracket starts
// HTML.
func TestReadmeTablesEscapeDescriptions(t *testing.T) {
	readme, err := GenerateReadme(loadResource(t, "yandex_airflow_cluster"), readmeOptions("yandex_airflow_cluster"))
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	src := string(readme)
	if !strings.Contains(src, "Apache Airflow version in format `&lt;major>.&lt;minor>`.") {
		t.Errorf("angle brackets are not escaped:\n%s", src)
	}
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "|---") {
			continue
		}
		if n := strings.Count(line, " | "); n != 4 {
			t.Errorf("row has %d column separators, want 4: %s", n, line)
		}
	}
}

// TestReadmeWithoutDocDropsTheDescriptionColumn covers -doc=false: the tables
// keep the mechanical facts and lose the prose. The section that says what the
// module creates stays: it is the point of the file, not a column.
func TestReadmeWithoutDocDropsTheDescriptionColumn(t *testing.T) {
	opt := readmeOptions("yandex_vpc_subnet")
	opt.Doc = false
	readme, err := GenerateReadme(loadResource(t, "yandex_vpc_subnet"), opt)
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	src := string(readme)
	if !strings.HasPrefix(src, "# Модуль yandex_vpc_subnet\n\n## Описание\n\nManages a subnet within the Yandex Cloud.") {
		t.Errorf("-doc=false dropped the resource description:\n%s", src)
	}
	if strings.Contains(src, "| Описание |") || strings.Contains(src, "The resource description.") {
		t.Errorf("-doc=false still emits an attribute description:\n%s", src)
	}
	if !strings.Contains(src, "| Атрибут | Тип | Обяз. | По умолчанию |\n|---|---|---|---|\n") {
		t.Errorf("table header is not the four-column one:\n%s", src)
	}
}

// TestReadmeWithoutRequiredAttributes covers a resource the caller can create
// with no arguments at all.
func TestReadmeWithoutRequiredAttributes(t *testing.T) {
	readme, err := GenerateReadme(loadResource(t, "yandex_vpc_network"), readmeOptions("yandex_vpc_network"))
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	src := string(readme)
	if !strings.Contains(src, "Обязательных атрибутов у ресурса нет") {
		t.Errorf("README does not say that nothing is mandatory:\n%s", src)
	}
	usage := flatten(hclFence(t, src))
	for _, want := range []string{"vpc_network = {", "example = {}"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage block is not an empty instance, missing %q:\n%s", want, src)
		}
	}
}

// TestReadmeLinksResolve checks the one thing a reader navigates by: every
// table link must reach the section it names. The headings are path-spelled,
// and GitHub drops the dots, so a mismatch is invisible until someone clicks.
// A description carrying its own headings must not shadow them either.
func TestReadmeLinksResolve(t *testing.T) {
	p := providerSchema(t)
	for _, name := range p.ResourceNames() {
		res, err := p.Resource(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		readme, err := GenerateReadme(res, readmeOptions(name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sections := map[string]bool{}
		other := map[string]bool{}
		for _, line := range strings.Split(string(readme), "\n") {
			level := 0
			for level < len(line) && line[level] == '#' {
				level++
			}
			if level == 0 || level == 1 || level == 2 || !strings.HasPrefix(line[level:], " ") {
				continue
			}
			slug := githubSlug(strings.ReplaceAll(strings.TrimSpace(line[level:]), "`", ""))
			if level == 3 {
				sections[slug] = true
			} else {
				other[slug] = true
			}
			if level > 6 {
				t.Errorf("%s: heading deeper than H6: %s", name, line)
			}
		}
		for slug := range sections {
			if other[slug] {
				t.Errorf("%s: a description heading shadows the section #%s", name, slug)
			}
		}
		// Only the first column is ours: a schema description may carry a
		// markdown link of its own, which is reproduced as the provider
		// wrote it.
		links := 0
		for _, line := range strings.Split(string(readme), "\n") {
			cell, _, ok := strings.Cut(strings.TrimPrefix(line, "| "), " | ")
			if !ok || !strings.HasPrefix(cell, "[`") {
				continue
			}
			_, rest, ok := strings.Cut(cell, "](#")
			target, ok2 := strings.CutSuffix(rest, ")")
			if !ok || !ok2 {
				t.Fatalf("%s: malformed link cell %q", name, cell)
			}
			links++
			if !sections[target] {
				t.Errorf("%s: link #%s reaches no section", name, target)
			}
		}
		if links != len(sections) {
			t.Errorf("%s: %d links for %d sections", name, links, len(sections))
		}
	}
}

// TestReadmeEmptyObjectHasNoTable covers an object whose fields are all
// computed: the caller sets it empty, so a table would carry a header and
// nothing else. The type says as much.
func TestReadmeEmptyObjectHasNoTable(t *testing.T) {
	readme, err := GenerateReadme(loadResource(t, "yandex_connectionmanager_connection"), readmeOptions("yandex_connectionmanager_connection"))
	if err != nil {
		t.Fatalf("GenerateReadme: %v", err)
	}
	src := string(readme)
	const heading = "### `connectionmanager_connection.params.clickhouse.cluster.tls_params.disabled`\n\n`object({})`\n"
	start := strings.Index(src, heading)
	if start < 0 {
		t.Fatalf("empty object section missing:\n%s", src)
	}
	rest := src[start+len(heading):]
	next := strings.Index(rest, "### ")
	if next < 0 {
		next = len(rest)
	}
	if strings.Contains(rest[:next], "| Атрибут |") {
		t.Errorf("empty object carries a table:\n%s", rest[:next])
	}
}

// TestReadmeUsageMatchesTFVarsExample pins the invariant behind
// -tfvars-optional: the module call in the README is the example file wrapped
// in a module block, in either mode. Both files ship together and a reader who
// copies one must get the values of the other.
func TestReadmeUsageMatchesTFVarsExample(t *testing.T) {
	for _, tc := range []struct {
		resource string
		optional bool
	}{
		{"yandex_vpc_subnet", false},
		{"yandex_vpc_subnet", true},
		{"yandex_compute_instance", true},
		{"yandex_vpc_network", true}, // nothing is mandatory: comments only
	} {
		label := tc.resource
		if tc.optional {
			label += " -tfvars-optional"
		}
		res := loadResource(t, tc.resource)
		opt := readmeOptions(tc.resource)
		opt.Optional = tc.optional
		opt.Defaults = NewDefaults(map[string]any{"description": "managed by tofu-resgen"})

		readme, err := GenerateReadme(res, opt)
		if err != nil {
			t.Fatalf("%s: GenerateReadme: %v", label, err)
		}
		usage := hclFence(t, string(readme))
		if _, diags := hclparse.NewParser().ParseHCL([]byte(usage), "README.md"); diags.HasErrors() {
			t.Fatalf("%s: usage block does not parse: %s\n%s", label, diags, usage)
		}
		tfvars, err := GenerateTFVars(res, TFVarsOptions{
			VarName:      opt.VarName,
			ResourceName: tc.resource,
			Optional:     tc.optional,
			Defaults:     opt.Defaults,
		})
		if err != nil {
			t.Fatalf("%s: GenerateTFVars: %v", label, err)
		}

		example := flattenLines(string(tfvars))
		start := slices.Index(example, opt.VarName+" = {")
		if start < 0 {
			t.Fatalf("%s: example file has no assignment:\n%s", label, tfvars)
		}
		want := example[start:]

		lines := flattenLines(usage)
		if len(lines) < 3 || lines[0] != fmt.Sprintf("module %q {", opt.VarName) || !strings.HasPrefix(lines[1], "source = ") {
			t.Fatalf("%s: unexpected module block:\n%s", label, usage)
		}
		got := lines[2 : len(lines)-1] // drop the module wrapper and its brace
		if !slices.Equal(want, got) {
			t.Errorf("%s: the README call and the example file diverge:\nwant %q\ngot  %q", label, want, got)
		}

		commented := slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "# ") })
		if commented != tc.optional {
			t.Errorf("%s: commented optional lines = %v, want %v:\n%s", label, commented, tc.optional, usage)
		}
		src := string(readme)
		if tc.optional != strings.Contains(src, "optional-атрибуты — закомментированными") {
			t.Errorf("%s: the usage note does not match the example:\n%s", label, src)
		}
		if tc.optional == strings.Contains(src, "Передаются только обязательные параметры") {
			t.Errorf("%s: the usage note does not match the example:\n%s", label, src)
		}
	}
}

// flattenLines collapses the indentation hclwrite adds, so that two renderings
// of the same example compare equal without pinning their alignment.
func flattenLines(src string) []string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// TestReadmeRequiresVarName covers the option the file cannot do without.
func TestReadmeRequiresVarName(t *testing.T) {
	if _, err := GenerateReadme(loadResource(t, "yandex_vpc_network"), ReadmeOptions{}); err == nil {
		t.Fatal("expected error without a variable name")
	}
}

// hclFence extracts the first ```hcl block of the README.
func hclFence(t *testing.T, src string) string {
	t.Helper()
	_, rest, ok := strings.Cut(src, "```hcl\n")
	if !ok {
		t.Fatalf("no hcl block in:\n%s", src)
	}
	body, _, ok := strings.Cut(rest, "```")
	if !ok {
		t.Fatalf("unterminated hcl block in:\n%s", src)
	}
	return body
}

// flatten collapses the indentation hclwrite adds, so that a test can compare
// lines without pinning the alignment.
func flatten(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(lines, "\n")
}

// githubSlug mirrors the anchor GitHub derives from a heading: lower case,
// punctuation dropped, spaces hyphenated. Modern table links depend on it.
func githubSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// fixtureProvider is the provider address of testdata/yandex.json.
const fixtureProvider = "registry.opentofu.org/yandex-cloud/yandex"

// deriveTestVarName mirrors the CLI rule for the variable name of a resource.
func deriveTestVarName(resource string) string {
	name := strings.TrimPrefix(resource, "yandex_")
	name = strings.TrimSuffix(name, "_instance")
	if name == "" {
		return resource
	}
	return name
}
