package language

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTSFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/service.ts", `export interface Config {
  enabled: boolean;
}

export enum Status {
  Ready,
}

export class Service {
  run(config: Config) {
    return config.enabled && validate(config);
  }
}

export const handler = () => new Service().run({ enabled: true });

export const workers = {
  resolve: () => handler(),
};
`)
	write("src/view.tsx", `import { Service } from "./service";

const service = new Service();

export const View = () => (
  <button onClick={() => service.run({ enabled: true })}>run</button>
);

export function submit() {
  return service.run({ enabled: true });
}
`)
	write("src/format.js", `export function formatValue(value) {
  return String(value);
}
`)
	write("src/service.test.ts", `export function ignoredTestHelper() {
  return true;
}
`)
	write("src/package.json", `{"scripts":{"test":"node --test"}}`)
	write("src/data.csv", "name,value\nalpha,1\n")
	return dir
}

func findDef(defs []IndexedDefinition, ident string) *IndexedDefinition {
	for i := range defs {
		if defs[i].Name == ident {
			return &defs[i]
		}
	}
	return nil
}

func TestScanTreeSitterRepository_TypeScriptDefsRefsAndSkips(t *testing.T) {
	dir := writeTSFixture(t)
	ex := ScanRepository(dir)

	if _, ok := ex.Definitions["src/service.test.ts"]; ok {
		t.Error("test files must be skipped")
	}
	if _, ok := ex.Definitions["src/package.json"]; ok {
		t.Error("file-scope data files must not enter the symbol index")
	}
	if _, ok := ex.Definitions["src/data.csv"]; ok {
		t.Error("non-reviewable files must not enter the symbol index")
	}
	serviceDefs := ex.Definitions["src/service.ts"]
	for _, ident := range []string{"Config", "Status", "Service", "Service.run", "handler", "workers"} {
		if findDef(serviceDefs, ident) == nil {
			t.Errorf("%s not extracted: %+v", ident, serviceDefs)
		}
	}
	run := findDef(serviceDefs, "Service.run")
	if run == nil {
		t.Fatal("Service.run not extracted")
	}
	if run.SymbolID != "src/service.ts::Service.run" {
		t.Errorf("SymbolID = %q", run.SymbolID)
	}
	if !strings.Contains(run.Signature, "run(config: Config)") {
		t.Errorf("Signature = %q", run.Signature)
	}
	if findDef(ex.Definitions["src/view.tsx"], "View") == nil || findDef(ex.Definitions["src/view.tsx"], "submit") == nil {
		t.Errorf("TSX defs missing: %+v", ex.Definitions["src/view.tsx"])
	}
	if findDef(ex.Definitions["src/format.js"], "formatValue") == nil {
		t.Errorf("JavaScript defs missing: %+v", ex.Definitions["src/format.js"])
	}
	if referenceCount(ex, "src/view.tsx", "run") < 2 {
		t.Errorf("view.tsx should reference run >=2 times, got %d", referenceCount(ex, "src/view.tsx", "run"))
	}
}

func TestScanTreeSitterRepository_TypeScriptDoesNotNeedNode(t *testing.T) {
	t.Setenv("PATH", "")
	ex := ScanRepository(writeTSFixture(t))
	if findDef(ex.Definitions["src/service.ts"], "Service.run") == nil {
		t.Fatalf("TypeScript scan should work without Node: %+v", ex.Definitions["src/service.ts"])
	}
}

func TestScanRepository_MergesTypeScript(t *testing.T) {
	dir := writeTSFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc Entrypoint() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ex := ScanRepository(dir)
	if len(ex.Definitions["main.go"]) != 1 {
		t.Errorf("Go defs missing from merged scan: %+v", ex.Definitions["main.go"])
	}
	if findDef(ex.Definitions["src/service.ts"], "Service.run") == nil {
		t.Errorf("TypeScript defs missing from merged scan: %+v", ex.Definitions["src/service.ts"])
	}
}
