// Command tofu-resgen generates a module's variables.tf from a provider schema
// produced by `tofu providers schema -json`.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ivaninkv/tofu-resgen/internal/schema"
	"github.com/ivaninkv/tofu-resgen/internal/tfgen"
	"github.com/ivaninkv/tofu-resgen/internal/verify"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type config struct {
	schemaPath     string
	providerAddr   string
	resource       string
	varName        string
	defaultsPath   string
	out            string
	module         string
	description    string
	doc            bool
	check          bool
	all            bool
	tofuBin        string
	refreshSchema  bool
	tfvarsOptional bool
}

// tfvarsExampleFile is the example variable file written by -module. It is not
// terraform.tfvars: OpenTofu loads that one automatically, and an example full
// of placeholders would break every plan.
const tfvarsExampleFile = "terraform.tfvars.example"

// readmeFile documents the generated module: what it creates, how to call it
// from git, and what the module variable holds.
const readmeFile = "README.md"

func run(args []string) int {
	fs := flag.NewFlagSet("tofu-resgen", flag.ContinueOnError)
	var cfg config
	fs.StringVar(&cfg.schemaPath, "schema", "", "path to a JSON schema produced by 'tofu providers schema -json'")
	fs.StringVar(&cfg.providerAddr, "provider", "", "provider address, e.g. yandex-cloud/yandex (optional when the schema has one provider)")
	fs.StringVar(&cfg.resource, "resource", "", "resource type to generate, e.g. yandex_compute_instance")
	fs.StringVar(&cfg.varName, "var-name", "", "module variable name (default derived from the resource type)")
	fs.StringVar(&cfg.defaultsPath, "defaults", "", "YAML file with defaults for optional attributes")
	fs.StringVar(&cfg.out, "out", "-", "output file, or '-' for stdout; with -all, an output directory")
	fs.StringVar(&cfg.module, "module", "", "generate a module directory: variables.tf, main.tf, terraform.tfvars.example and README.md (one resource, or one subdirectory per resource with -all)")
	fs.BoolVar(&cfg.tfvarsOptional, "tfvars-optional", false, "also show optional attributes, commented out, in the example tfvars and in the README usage example")
	fs.StringVar(&cfg.description, "description", "", "override the generated variable description")
	fs.BoolVar(&cfg.doc, "doc", true, "emit schema descriptions as comments in variables.tf and as a column of the README tables")
	fs.BoolVar(&cfg.check, "check", false, "verify the generated variables against the schema instead of writing")
	fs.BoolVar(&cfg.all, "all", false, "generate a file per resource into the -out directory")
	fs.StringVar(&cfg.tofuBin, "tofu-bin", "tofu", "tofu binary used by -refresh-schema")
	fs.BoolVar(&cfg.refreshSchema, "refresh-schema", false, "run 'tofu providers schema -json' and cache it to -schema")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	outSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "out" {
			outSet = true
		}
	})
	if outSet && cfg.module != "" {
		fmt.Fprintln(os.Stderr, "tofu-resgen: -out and -module are mutually exclusive")
		return 2
	}

	if cfg.refreshSchema {
		if cfg.providerAddr == "" {
			fmt.Fprintln(os.Stderr, "tofu-resgen: -refresh-schema requires -provider")
			return 2
		}
		if cfg.schemaPath == "" {
			fmt.Fprintln(os.Stderr, "tofu-resgen: -refresh-schema requires -schema (cache destination)")
			return 2
		}
		if err := refreshSchema(cfg.tofuBin, cfg.providerAddr, cfg.schemaPath); err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
	}
	if cfg.schemaPath == "" {
		fmt.Fprintln(os.Stderr, "tofu-resgen: -schema is required")
		return 2
	}

	ps, err := schema.Load(cfg.schemaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	addr, provider, err := ps.Provider(cfg.providerAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}

	if cfg.all {
		if cfg.module != "" {
			return runAllModules(cfg, addr, provider)
		}
		return runAll(cfg, addr, provider)
	}
	if cfg.resource == "" {
		fmt.Fprintln(os.Stderr, "tofu-resgen: -resource is required (or use -all)")
		return 2
	}
	varName := cfg.varName
	if varName == "" {
		varName = deriveVarName(addr, cfg.resource)
	}
	if cfg.module != "" {
		return runModule(cfg, addr, provider, cfg.resource, varName)
	}
	code, err := generateOne(cfg, addr, provider, cfg.resource, varName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return code
	}
	return 0
}

func generateOne(cfg config, addr string, provider schema.ProviderSchema, resource, varName string) (int, error) {
	res, err := provider.Resource(resource)
	if err != nil {
		return 2, err
	}
	var defaults *tfgen.Defaults
	if cfg.defaultsPath != "" {
		defaults, _, err = loadDefaults(cfg, res)
		if err != nil {
			return 3, err
		}
	}
	out, err := tfgen.Generate(res, tfgen.Options{
		VarName:      varName,
		ResourceName: resource,
		Description:  cfg.description,
		Doc:          cfg.doc,
		Defaults:     defaults,
	})
	if err != nil {
		return 2, err
	}
	if cfg.check {
		problems, err := verify.Conforms(out, res, varName, defaults)
		if err != nil {
			return 2, err
		}
		if len(problems) > 0 {
			fmt.Fprintf(os.Stderr, "%s: %d schema deviation(s):\n", resource, len(problems))
			for _, p := range problems {
				fmt.Fprintf(os.Stderr, "  - %s\n", p)
			}
			return 1, nil
		}
		fmt.Printf("%s: variables for %q conform to the schema\n", resource, varName)
		return 0, nil
	}
	if cfg.out == "-" || cfg.out == "" {
		_, err := os.Stdout.Write(out)
		return 2, err
	}
	if err := os.WriteFile(cfg.out, out, 0o644); err != nil {
		return 2, fmt.Errorf("write %s: %w", cfg.out, err)
	}
	return 0, nil
}

// loadDefaults reads and validates the defaults file, or returns nil when
// -defaults is not set.
func loadDefaults(cfg config, res schema.Schema) (*tfgen.Defaults, int, error) {
	if cfg.defaultsPath == "" {
		return nil, 0, nil
	}
	d, err := tfgen.LoadDefaults(cfg.defaultsPath)
	if err != nil {
		return nil, 3, err
	}
	if err := d.Validate(res); err != nil {
		return nil, 3, err
	}
	return d, 0, nil
}

// runModule writes or checks a module directory for one resource:
// variables.tf, main.tf, terraform.tfvars.example and README.md.
func runModule(cfg config, addr string, provider schema.ProviderSchema, resource, varName string) int {
	res, err := provider.Resource(resource)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	if cfg.check {
		return checkModule(cfg, addr, res, resource, varName)
	}
	return writeModule(cfg, addr, res, resource, varName)
}

// runAllModules writes or checks one module directory per resource, each with
// its own example tfvars and README (one defaults file cannot describe every
// resource, and a README documents one resource).
func runAllModules(cfg config, addr string, provider schema.ProviderSchema) int {
	if !cfg.check {
		if err := os.MkdirAll(cfg.module, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
	}
	for _, resource := range provider.ResourceNames() {
		res, err := provider.Resource(resource)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
		sub := cfg
		sub.module = filepath.Join(cfg.module, resource)
		// One defaults file cannot describe the module of every resource.
		sub.defaultsPath = ""
		varName := deriveVarName(addr, resource)
		if cfg.check {
			if code := checkModule(sub, addr, res, resource, varName); code != 0 {
				return code
			}
			continue
		}
		if code := writeModule(sub, addr, res, resource, varName); code != 0 {
			return code
		}
	}
	return 0
}

func writeModule(cfg config, addr string, res schema.Schema, resource, varName string) int {
	defaults, code, err := loadDefaults(cfg, res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return code
	}
	vars, err := tfgen.Generate(res, tfgen.Options{
		VarName:      varName,
		ResourceName: resource,
		Description:  cfg.description,
		Doc:          cfg.doc,
		Defaults:     defaults,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	mainTF, err := tfgen.GenerateMain(res, tfgen.MainOptions{
		VarName:      varName,
		ResourceName: resource,
		ProviderAddr: addr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	tfvars, err := tfgen.GenerateTFVars(res, tfgen.TFVarsOptions{
		VarName:      varName,
		ResourceName: resource,
		Defaults:     defaults,
		Optional:     cfg.tfvarsOptional,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	readme, err := tfgen.GenerateReadme(res, tfgen.ReadmeOptions{
		VarName:      varName,
		ResourceName: resource,
		ProviderAddr: addr,
		Description:  cfg.description,
		Doc:          cfg.doc,
		Optional:     cfg.tfvarsOptional,
		Defaults:     defaults,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	if err := os.MkdirAll(cfg.module, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	files := []struct {
		name    string
		content []byte
	}{{"variables.tf", vars}, {"main.tf", mainTF}, {tfvarsExampleFile, tfvars}, {readmeFile, readme}}
	for _, f := range files {
		path := filepath.Join(cfg.module, f.name)
		if err := os.WriteFile(path, f.content, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: write %s: %v\n", path, err)
			return 2
		}
		fmt.Println(path)
	}
	return 0
}

// checkModule verifies the module already on disk. The defaults file, when
// given, describes the defaults already baked into variables.tf.
//
// README.md is prose: it is regenerated rather than checked, so a deviation
// there is a documentation fix, not a schema deviation.
func checkModule(cfg config, addr string, res schema.Schema, resource, varName string) int {
	defaults, _, err := loadDefaults(cfg, res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 3
	}
	paths := []string{
		filepath.Join(cfg.module, "variables.tf"),
		filepath.Join(cfg.module, "main.tf"),
	}
	contents := make([][]byte, len(paths))
	for i, path := range paths {
		contents[i], err = os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
	}
	problems, err := verify.Conforms(contents[0], res, varName, defaults)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	mainProblems, err := verify.MainConforms(contents[1], contents[0], res, addr, resource, varName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	problems = append(problems, mainProblems...)

	// The example tfvars is a starting point the author edits, so it is
	// checked for conformance only, and only when it is there.
	tfvarsPath := filepath.Join(cfg.module, tfvarsExampleFile)
	if example, err := os.ReadFile(tfvarsPath); err == nil {
		exampleProblems, err := verify.TFVarsConforms(example, res, varName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
		problems = append(problems, exampleProblems...)
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
		return 2
	}
	if len(problems) > 0 {
		fmt.Fprintf(os.Stderr, "%s: %d schema deviation(s):\n", resource, len(problems))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  - %s\n", p)
		}
		return 1
	}
	fmt.Printf("%s: module %s conforms to the schema\n", resource, cfg.module)
	return 0
}

func runAll(cfg config, addr string, provider schema.ProviderSchema) int {
	dir := cfg.out
	if !cfg.check {
		if dir == "" || dir == "-" {
			fmt.Fprintln(os.Stderr, "tofu-resgen: -all requires -out <directory>")
			return 2
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
	}
	for _, resource := range provider.ResourceNames() {
		varName := deriveVarName(addr, resource)
		res, err := provider.Resource(resource)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
		out, err := tfgen.Generate(res, tfgen.Options{
			VarName:      varName,
			ResourceName: resource,
			Doc:          cfg.doc,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %s: %v\n", resource, err)
			return 2
		}
		if cfg.check {
			problems, err := verify.Conforms(out, res, varName, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "tofu-resgen: %s: %v\n", resource, err)
				return 2
			}
			for _, p := range problems {
				fmt.Fprintf(os.Stderr, "%s: %s\n", resource, p)
			}
			continue
		}
		path := filepath.Join(dir, resource+".tf")
		if err := os.WriteFile(path, out, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "tofu-resgen: %v\n", err)
			return 2
		}
		fmt.Println(path)
	}
	return 0
}

// deriveVarName turns a resource type into a module variable name by dropping
// the provider prefix and a trailing "_instance":
// yandex_compute_instance -> compute.
func deriveVarName(providerAddr, resource string) string {
	name := resource
	if local := schema.LocalName(providerAddr); local != "" {
		name = strings.TrimPrefix(name, local+"_")
	}
	name = strings.TrimSuffix(name, "_instance")
	if name == "" {
		return resource
	}
	return name
}

func refreshSchema(bin, providerAddr, outPath string) error {
	dir, err := os.MkdirTemp("", "tofu-resgen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	local := schema.LocalName(providerAddr)
	mainTF := fmt.Sprintf("terraform {\n  required_providers {\n    %s = {\n      source = %q\n    }\n  }\n}\n", local, providerAddr)
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(mainTF), 0o644); err != nil {
		return err
	}
	if out, err := runCmd(dir, bin, "init", "-backend=false", "-input=false"); err != nil {
		return fmt.Errorf("%s init failed: %w\n%s", bin, err, out)
	}
	out, err := runCmd(dir, bin, "providers", "schema", "-json")
	if err != nil {
		return fmt.Errorf("%s providers schema failed: %w\n%s", bin, err, out)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(out), &probe); err != nil {
		return fmt.Errorf("%s returned invalid schema JSON: %w", bin, err)
	}
	if err := os.WriteFile(outPath, []byte(out), 0o644); err != nil {
		return err
	}
	return nil
}

func runCmd(dir, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if errors.Is(err, exec.ErrNotFound) {
		return string(out), fmt.Errorf("binary %q not found", bin)
	}
	return string(out), err
}
