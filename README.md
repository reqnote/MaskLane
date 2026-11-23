# MaskLane

Kiro IDE が送る本文に含まれる登録済みのキーワードを、Windows 上でマスキングする試用版です。Pipelock 3.8.0 の辞書置換と HTTPS プロキシを利用しています。

## Windows での試用

[Releases](https://github.com/reqnote/MaskLane/releases) から Windows x64 用の ZIP を入手して展開します。試用担当者が証明書と Kiro の接続を準備した後、利用者がチャットの直接入力と TXT ファイル参照を試します。セットアップ、Kiro のステアリング、終了時の設定復元は [Windows 試用ガイド](pilot/Windows-試用ガイド.txt) に記載しています。

- 日本語の画面で、キーワードを「企業名」「案件名」「個人名」「社外秘」「その他」に分けて登録できます。「置換後の表記」を空欄にすると、選んだ種類に応じて `<pl:person:1>` などの表記が自動で生成されます。
- 選んだ項目を JSON で共有できます。取り込み時は、既存の設定を優先します。
- 一覧の「置換後の表記」には、送信時に自動生成された表記が表示されます。「最近の通信」で別の送信に切り替えて、応答と照合できます。履歴は時刻、置換数、送信結果、自動生成された表記を実行中だけ保持します。

試用版は Kiro の米国東部と欧州のモデル用エンドポイントを対象としています。ローカルの模擬 HTTPS 通信で置換と検査失敗時の停止を確認しています。Kiro IDE 実機でチャットと TXT の両経路を確認するまでは、試用に架空のキーワードを使ってください。

## ソースからビルド

Windows x64 で Git、PowerShell、`curl.exe`、`tar.exe` を使います。リポジトリのルートで次を実行します。

```powershell
.\build.ps1 -Test
```

ビルドは、固定した Pipelock ソースと Go ツールチェーンを取得してハッシュを検証し、パッチと `overlay` の内容を適用します。`-Test` は置換、HTTPS の模擬通信、日本語画面の API を検証します。`MaskLane.exe` と `MaskLane-Windows-x64.zip` は `dist` に生成されます。

## Pipelock

この試用版は Pipelock 3.8.0 のオープンソース部分を使用しています。利用条件と著作権表示は [LICENSE](third_party/pipelock/LICENSE)・[NOTICE](third_party/pipelock/NOTICE) を参照してください。[Pipelock のソース](https://github.com/pipelab-org/pipelock/tree/556048c1c18d84f7d61d28f686220178a4ff2c4d)
