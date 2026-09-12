// Command extract は 1Password shell-plugins からカタログ候補JSONを生成する。
//
// 使い方: go run ./tools/extract /path/to/shell-plugins > candidates.json
// 出力は人間レビュー用。採用分だけ catalog/v1/ に手でマージする。
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type candidate struct {
	Plugin     string   `json:"plugin"`
	Platform   string   `json:"platform,omitempty"`
	Homepage   string   `json:"homepage,omitempty"`
	Prefix     string   `json:"prefix,omitempty"`
	EnvVars    []string `json:"envVars,omitempty"`
	DocsURL    string   `json:"docsUrl,omitempty"`
	MgmtURL    string   `json:"managementUrl,omitempty"`
}

func strLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind.String() != "STRING" {
		return "", false
	}
	return strings.Trim(lit.Value, `"`), true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: extract <shell-plugins-dir>")
		os.Exit(2)
	}
	root := filepath.Join(os.Args[1], "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var out []candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		c := candidate{Plugin: e.Name()}
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			extractFile(f, &c)
		}
		if c.Prefix != "" || len(c.EnvVars) > 0 {
			sort.Strings(c.EnvVars)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

func extractFile(path string, c *candidate) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		switch key.Name {
		case "Prefix":
			if s, ok := strLit(kv.Value); ok && c.Prefix == "" {
				c.Prefix = s
			}
		case "DocsURL", "ManagementURL":
			if call, ok := kv.Value.(*ast.CallExpr); ok {
				for _, a := range call.Args {
					if s, ok := strLit(a); ok && strings.HasPrefix(s, "http") {
						if key.Name == "DocsURL" && c.DocsURL == "" {
							c.DocsURL = s
						}
						if key.Name == "ManagementURL" && c.MgmtURL == "" {
							c.MgmtURL = s
						}
					}
				}
			}
		case "Name":
			// Plugin名・Platform名の候補。既存値がなければ採用。
			if s, ok := strLit(kv.Value); ok && s != "" {
				_ = s
			}
		}
		// 環境変数マッピング: "XXX_API_KEY": fieldname.Y のキー側
		if key.Name == "" {
			return true
		}
		return true
	})
	// env var抽出: "AAA_BBB": fieldname.XXX パターン
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		name, ok := strLit(kv.Key)
		if !ok || !isEnvName(name) {
			return true
		}
		if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "fieldname") {
				if !contains(c.EnvVars, name) {
					c.EnvVars = append(c.EnvVars, name)
				}
			}
		}
		return true
	})
	// Platform名・Homepage: schema.Plugin リテラル内のみ
	ast.Inspect(f, func(n ast.Node) bool {
		comp, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := comp.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Plugin" {
			return true
		}
		for _, el := range comp.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			if k.Name == "Name" {
				if s, ok := strLit(kv.Value); ok && s != "" {
					c.Platform = s
				}
			}
			if k.Name == "Platform" {
				if inner, ok := kv.Value.(*ast.CompositeLit); ok {
					ast.Inspect(inner, func(n2 ast.Node) bool {
						call, ok := n2.(*ast.CallExpr)
						if !ok {
							return true
						}
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "URL" {
							return true
						}
						for _, a := range call.Args {
							if s, ok := strLit(a); ok && strings.HasPrefix(s, "http") && c.Homepage == "" {
								c.Homepage = s
							}
						}
						return true
					})
					for _, el2 := range inner.Elts {
						kv2, ok := el2.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k2, ok := kv2.Key.(*ast.Ident)
						if !ok {
							continue
						}
						if k2.Name == "Name" {
							if s, ok := strLit(kv2.Value); ok {
								c.Platform = s
							}
						}
					}
				}
			}
		}
		return true
	})
	// HomepageはPlatform走査で取得済み。ここでは何もしない。
}

func isEnvName(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return strings.Contains(s, "_")
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
