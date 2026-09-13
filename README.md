# keylake-catalog

KeyLake のカタログ正本。APIキーの接頭辞マップとサービス一覧を配信する。
KeyLakeアプリはタグ固定URLで取得する（例: `.../v1.0.0/catalog/v1/key-prefixes.json`）。

## ディレクトリ

- `catalog/v1/` — 配信用JSON（正本）。アプリは `plugins.json` 1本だけ取得する
- `plugins/` — 上流 (1Password shell-plugins) の正規化ミラー。1プラグイン1ファイルで上流スキーマに1:1対応
- `keylake-map.json` — 上流プラグイン名→KeyLake serviceId対応（空は未対応）
- `tools/extract/` — 旧抽出ツール（接頭辞候補用）
- `tools/normalize/` — 正規化ツール。上流GoソースをAST解析して `plugins/*.json` + bundleを生成

## 上流ソース（すべてMIT）

- [1Password shell-plugins](https://github.com/1Password/shell-plugins) — 接頭辞・環境変数・公式リンクの参照元
- [OpenUsage](https://github.com/janekbaraniewski/openusage) — 利用量取得方式の参照元（カタログデータ自体は使わない）

## 更新手順（正規化ミラー）

1. 上流を取得: `git clone https://github.com/1Password/shell-plugins.git`（特定コミット推奨）
2. `go run ./tools/normalize -plugins <checkout> -sha <commit> -out .` で `plugins/*.json` + `catalog/v1/plugins.json` を再生成
3. `git diff` で差分を目視精査する（上流の変更はそのまま差分として現れる）
4. `keylake-map.json` の空欄を必要に応じて埋める（人間判断）
5. タグを切る（`v1.0.0` → `v1.1.0` …）。アプリ側はタグ固定で取得する
6. 破壊的変更は `catalog/v2/` に上げる

## 更新手順（旧・接頭辞/サービス一覧）

1. `tools/extract` で上流から候補JSONを生成
2. 差分を目視精査し、採用分だけ `catalog/v1/` にマージ（confidence付けは人間判断）
3. タグを切る（`v1.0.0` → `v1.1.0` …）。アプリ側はタグ固定で取得する
4. 破壊的変更は `catalog/v2/` に上げる

## 形式メモ

- `plugins/*.json` のフィールド名は上流 `schema.Plugin` 系に準拠（name / platform / credentials{prefix, charset, envVars, docsUrl, managementUrl} / executables{runs}）
- `NeedsAuth` は関数値のためJSON化不可。対象外とし、KeyLake側の信頼判断を使う
- 秘密値（キー本体）は絶対に含めない。接頭辞のみ。

## コントリビューション

接頭辞の追加・修正はPRで。形式は既存JSONに合わせること。
秘密値（キー本体）は絶対に含めない。接頭辞のみ。
