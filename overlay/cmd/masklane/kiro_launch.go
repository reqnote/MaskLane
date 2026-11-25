package main

import (
	"fmt"
	"strings"
)

const kiroLaunchScript = `& {
	if (Get-Process -Name Kiro -ErrorAction SilentlyContinue) {
		throw 'Kiro を完全に終了してから、このコマンドを実行してください。'
	};
	$ca = %s;
	if (!(Test-Path -LiteralPath $ca -PathType Leaf)) {
		throw 'MaskLane の証明書が見つかりません。起動中の MaskLane の画面からコマンドをコピーし直してください。'
	};
	$appPaths = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\App Paths\Kiro.exe',
		'HKLM:\Software\Microsoft\Windows\CurrentVersion\App Paths\Kiro.exe',
		'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\App Paths\Kiro.exe';
	$installed = @(Get-Item -LiteralPath $appPaths -ErrorAction SilentlyContinue | ForEach-Object { $_.GetValue('') });
	$installed += "$env:LOCALAPPDATA\Programs\Kiro\Kiro.exe",
		"$env:ProgramFiles\Kiro\Kiro.exe", "@X86@\Kiro\Kiro.exe";
	$isKiro = {
		$_ -match '^(?:[a-z]:\\|\\\\)' -and [IO.Path]::GetFileName($_) -eq 'Kiro.exe' -and
			(Test-Path -LiteralPath $_ -PathType Leaf)
	};
	$kiro = $installed | Where-Object -FilterScript $isKiro | Select-Object -First 1;
	if (!$kiro) {
		$links = foreach ($folder in 'Programs', 'CommonPrograms') {
			$menu = [Environment]::GetFolderPath($folder);
			if ($menu) { Get-ChildItem -LiteralPath $menu -Filter 'Kiro.lnk' -File -Recurse -ErrorAction SilentlyContinue }
		};
		if ($links) {
			$shell = New-Object -ComObject WScript.Shell;
			$kiro = $links | ForEach-Object { $shell.CreateShortcut($_.FullName).TargetPath } |
				Where-Object -FilterScript $isKiro | Select-Object -First 1
		}
	};
	if (!$kiro) {
		$kiro = Get-Command Kiro.exe -CommandType Application -All -ErrorAction SilentlyContinue |
			Select-Object -ExpandProperty Source | Where-Object -FilterScript $isKiro | Select-Object -First 1
	};
	if (!$kiro) {
		$kiro = (Read-Host 'Kiro が見つかりませんでした。Kiro.exe のフルパス').Trim().Trim('"');
		if (!($kiro | Where-Object -FilterScript $isKiro)) { throw 'Kiro.exe のフルパスを確認してください。' }
	};
	$proxy = %s;
	$start = New-Object System.Diagnostics.ProcessStartInfo;
	$start.FileName = $kiro;
	$start.WorkingDirectory = [IO.Path]::GetDirectoryName($kiro);
	$start.UseShellExecute = $false;
	$start.EnvironmentVariables['NODE_EXTRA_CA_CERTS'] = $ca;
	$start.EnvironmentVariables['HTTPS_PROXY'] = $proxy;
	$start.EnvironmentVariables['HTTP_PROXY'] = $proxy;
	$start.EnvironmentVariables['NO_PROXY'] = '127.0.0.1,localhost';
	[void][System.Diagnostics.Process]::Start($start)
}`

func kiroLaunchCommand(proxyURL, certPath string) string {
	lines := strings.Split(kiroLaunchScript, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return fmt.Sprintf(strings.Join(lines, " "), powerShellLiteral(certPath), powerShellLiteral(proxyURL))
}

func powerShellLiteral(value string) string {
	// PowerShell recognizes typographic single quotes as delimiters too.
	quotes := strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
	return "'" + quotes.Replace(value) + "'"
}
