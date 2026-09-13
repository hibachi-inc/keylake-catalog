// Command normalize は 1Password shell-plugins から正規化JSONを生成する。
//
// 使い方: go run ./tools/normalize -plugins /path/to/shell-plugins -sha <commit> -out .
// 出力:
//
//	plugins/<name>.json      1プラグイン1ファイル (上流スキーマの機械的JSON化)
//	catalog/v1/plugins.json  配信用バンドル (アプリはこれだけ取得する)
//	keylake-map.json         上流名→KeyLake serviceId対応 (人間が埋める。空は未対応)
//
// NeedsAuth は関数値のためJSON化できない。KeyLake側のDirect Mode信頼判断を使うため対象外。
// 秘密値は扱わない (接頭辞・ charset・ env名・ URLのみ)。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type charset struct {
	Uppercase bool `json:"uppercase,omitempty"`
	Lowercase bool `json:"lowercase,omitempty"`
	Digits    bool `json:"digits,omitempty"`
	Symbols   bool `json:"symbols,omitempty"`
}

type credential struct {
	Name          string   `json:"name"`
	Prefix        string   `json:"prefix,omitempty"`
	Length        int      `json:"length,omitempty"`
	Charset       *charset `json:"charset,omitempty"`
	EnvVars       []string `json:"envVars,omitempty"`
	DocsURL       string   `json:"docsUrl,omitempty"`
	ManagementURL string   `json:"managementUrl,omitempty"`
}

type executable struct {
	Name    string   `json:"name"`
	Runs    []string `json:"runs,omitempty"`
	DocsURL string   `json:"docsUrl,omitempty"`
}

type plugin struct {
	Name        string       `json:"name"`
	ServiceID   string       `json:"serviceId,omitempty"`
	Category    string       `json:"category,omitempty"`
	Keywords    []string     `json:"keywords,omitempty"`
	Platform    string       `json:"platform,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Credentials []credential `json:"credentials,omitempty"`
	Executables []executable `json:"executables,omitempty"`
}

func strLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind.String() != "STRING" {
		return "", false
	}
	return strings.Trim(lit.Value, `"`), true
}

func boolLit(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "true"
}

func intLit(e ast.Expr) int {
	lit, ok := e.(*ast.BasicLit)
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(lit.Value)
	return n
}

func isEnvName(s string) bool {
	if len(s) < 2 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// sdkURL は sdk.URL("https://...") 呼び出しからURLを取り出す。
func sdkURL(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "URL" {
		return ""
	}
	for _, a := range call.Args {
		if s, ok := strLit(a); ok && strings.HasPrefix(s, "http") {
			return s
		}
	}
	return ""
}

func strSlice(e ast.Expr) []string {
	comp, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range comp.Elts {
		if s, ok := strLit(el); ok {
			out = append(out, s)
		}
	}
	return out
}

func selName(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

// kvEach は CompositeLit直下の {Key: Value} を走査する。
func kvEach(comp *ast.CompositeLit, fn func(key string, val ast.Expr)) {
	for _, el := range comp.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		fn(k.Name, kv.Value)
	}
}

func compLit(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op.String() == "&" {
		e = u.X
	}
	c, _ := e.(*ast.CompositeLit)
	return c
}

func typeSelName(e ast.Expr) string {
	c := compLit(e)
	if c == nil {
		return ""
	}
	sel, ok := c.Type.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

type fileInfo struct {
	creds []credential
	execs []executable
	envs  []string
}

func parseCharset(e ast.Expr) *charset {
	c := compLit(e)
	if c == nil {
		return nil
	}
	cs := &charset{}
	empty := true
	kvEach(c, func(k string, v ast.Expr) {
		switch k {
		case "Uppercase":
			cs.Uppercase = boolLit(v)
		case "Lowercase":
			cs.Lowercase = boolLit(v)
		case "Digits":
			cs.Digits = boolLit(v)
		case "Symbols":
			cs.Symbols = boolLit(v)
		}
		empty = false
	})
	if empty {
		return nil
	}
	return cs
}

func parseCredential(comp *ast.CompositeLit) credential {
	var c credential
	kvEach(comp, func(k string, v ast.Expr) {
		switch k {
		case "Name":
			if s := selName(v); s != "" {
				c.Name = s
			} else if s, ok := strLit(v); ok {
				c.Name = s
			}
		case "DocsURL":
			c.DocsURL = sdkURL(v)
		case "ManagementURL":
			c.ManagementURL = sdkURL(v)
		case "Fields":
			fc := compLit(v)
			if fc == nil {
				return
			}
			for _, el := range fc.Elts {
				f := compLit(el)
				if f == nil || typeSelName(el) != "CredentialField" && !isCredField(f) {
					continue
				}
				kvEach(f, func(fk string, fv ast.Expr) {
					if fk != "Composition" {
						return
					}
					cc := compLit(fv)
					if cc == nil {
						return
					}
					kvEach(cc, func(ck string, cv ast.Expr) {
						switch ck {
						case "Prefix":
							if s, ok := strLit(cv); ok && c.Prefix == "" {
								// URLや短すぎる接頭辞は誤判定の元なので落とす
								if len(s) >= 3 && !strings.HasPrefix(s, "http") {
									c.Prefix = s
								}
							}
						case "Length":
							c.Length = intLit(cv)
						case "Charset":
							if cs := parseCharset(cv); cs != nil {
								c.Charset = cs
							}
						}
					})
				})
			}
		}
	})
	return c
}

// isCredField は型名が書かれていないField要素の簡易判定 (Secretキーの有無)。
func isCredField(f *ast.CompositeLit) bool {
	found := false
	kvEach(f, func(k string, _ ast.Expr) {
		if k == "Secret" || k == "Composition" {
			found = true
		}
	})
	return found
}

func parseExecutable(comp *ast.CompositeLit) executable {
	var e executable
	kvEach(comp, func(k string, v ast.Expr) {
		switch k {
		case "Name":
			if s, ok := strLit(v); ok {
				e.Name = s
			}
		case "Runs":
			e.Runs = strSlice(v)
		case "DocsURL":
			e.DocsURL = sdkURL(v)
		}
	})
	return e
}

// collectEnv はファイル内の env名→fieldname マッピングと TryEnvVarPair 引数を集める。
func collectEnv(f *ast.File) []string {
	var out []string
	add := func(s string) {
		if !isEnvName(s) {
			return
		}
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if name, ok := strLit(kv.Key); ok {
			if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "fieldname" {
					add(name)
				}
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selName(call.Fun) != "TryEnvVarPair" {
			return true
		}
		for _, a := range call.Args {
			if s, ok := strLit(a); ok {
				add(s)
			}
		}
		return true
	})
	sort.Strings(out)
	return out
}

func parseFile(path string) fileInfo {
	var fi fileInfo
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return fi
	}
	ast.Inspect(f, func(n ast.Node) bool {
		comp, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		switch typeSelName(comp) {
		case "CredentialType":
			fi.creds = append(fi.creds, parseCredential(comp))
		case "Executable":
			fi.execs = append(fi.execs, parseExecutable(comp))
		}
		return true
	})
	fi.envs = collectEnv(f)
	return fi
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0644)
}

// loadServiceMap は keylake-map.json から上流名→対応付けを読む。
func loadServiceMap(out string) map[string]mapEntry {
	b, err := os.ReadFile(filepath.Join(out, "keylake-map.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Map map[string]mapEntry `json:"map"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m.Map
}

type mapEntry struct {
	ServiceID string   `json:"serviceId"`
	Category  string   `json:"category"`
	Keywords  []string `json:"keywords"`
}

func main() {
	pluginsDir := flag.String("plugins", "", "shell-plugins checkout dir")
	sha := flag.String("sha", "", "upstream commit sha")
	out := flag.String("out", ".", "catalog repo root")
	flag.Parse()
	if *pluginsDir == "" || *sha == "" {
		fmt.Fprintln(os.Stderr, "usage: normalize -plugins <dir> -sha <commit> [-out <catalog-root>]")
		os.Exit(2)
	}
	root := filepath.Join(*pluginsDir, "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var all []plugin
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		p := plugin{Name: e.Name()}
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fi := parseFile(f)
			// 同一ファイル内のenvは同ファイルのcredentialに付ける
			for i := range fi.creds {
				fi.creds[i].EnvVars = append(fi.creds[i].EnvVars, fi.envs...)
			}
			p.Credentials = append(p.Credentials, fi.creds...)
			p.Executables = append(p.Executables, fi.execs...)
			_ = f
		}
		// plugin.go の Name/Platform を拾う (Credentials等と同ファイルとは限らない)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			pf, err := parser.ParseFile(fset, f, nil, 0)
			if err != nil {
				continue
			}
			ast.Inspect(pf, func(n ast.Node) bool {
				comp, ok := n.(*ast.CompositeLit)
				if !ok || typeSelName(comp) != "Plugin" {
					return true
				}
				kvEach(comp, func(k string, v ast.Expr) {
					if k != "Platform" {
						return
					}
					pc := compLit(v)
					if pc == nil {
						return
					}
					kvEach(pc, func(pk string, pv ast.Expr) {
						switch pk {
						case "Name":
							if s, ok := strLit(pv); ok {
								p.Platform = s
							}
						case "Homepage":
							if u := sdkURL(pv); u != "" {
								p.Homepage = u
							}
						}
					})
				})
				return true
			})
		}
		if len(p.Credentials) == 0 && len(p.Executables) == 0 {
			continue
		}
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	svcMap := loadServiceMap(*out)
	for i := range all {
		e, ok := svcMap[all[i].Name]
		if !ok || e.ServiceID == "" {
			continue
		}
		all[i].ServiceID = e.ServiceID
		all[i].Category = e.Category
		all[i].Keywords = e.Keywords
	}

	plugDir := filepath.Join(*out, "plugins")
	if err := os.MkdirAll(plugDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, p := range all {
		if err := writeJSON(filepath.Join(plugDir, p.Name+".json"), p); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	bundle := map[string]any{
		"version": 1,
		"updated": time.Now().UTC().Format("2006-01-02"),
		"sources": []string{fmt.Sprintf("1Password/shell-plugins@%s (MIT)", *sha)},
		"notes":   "1Password shell-plugins の機械的JSON化。秘密値は含まない。NeedsAuthは関数値のため対象外。",
		"plugins": all,
	}
	if err := writeJSON(filepath.Join(*out, "catalog", "v1", "plugins.json"), bundle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("plugins: %d\n", len(all))
}
