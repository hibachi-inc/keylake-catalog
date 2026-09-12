# keylake-catalog

KeyLake のカタログ正本。APIキーの接頭辞マップとサービス一覧を配信する。
KeyLakeアプリはタグ固定URLで取得する（例: `.../v1.0.0/catalog/v1/key-prefixes.json`）。

## ディレクトリ

- `catalog/v1/` — 配信用JSON（正本）
- `tools/extract/` — 上流 (1Password shell-plugins) からの抽出ツール

## 上流ソース（すべてMIT）

- [1Password shell-plugins](https://github.com/1Password/shell-plugins) — 接頭辞・環境変数・公式リンクの参照元
- [OpenUsage](https://github.com/janekbaraniewski/openusage) — 利用量取得方式の参照元（カタログデータ自体は使わない）

## 更新手順

1. `tools/extract` で上流から候補JSONを生成
2. 差分を目視精査し、採用分だけ `catalog/v1/` にマージ（confidence付けは人間判断）
3. タグを切る（`v1.0.0` → `v1.1.0` …）。アプリ側はタグ固定で取得する
4. 破壊的変更は `catalog/v2/` に上げる

## コントリビューション

接頭辞の追加・修正はPRで。形式は既存JSONに合わせること。
秘密値（キー本体）は絶対に含めない。接頭辞のみ。
