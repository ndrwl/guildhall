# Prints the script-judged facts of a unified diff for a self-review, with no model and no tool.
# Usage: facts.ps1 <diff-file> [<budget-lines> [<budget-files>]]; a budget of 0 turns that half of the size flag off.
# Twin of facts.sh: keep the two equivalent.
$ErrorActionPreference = 'Stop'
function Fail-Usage([string]$why) {
    [Console]::Error.WriteLine("usage: facts.ps1 <diff-file> [<budget-lines> [<budget-files>]]: $why")
    exit 2
}
if ($args.Count -lt 1 -or $args.Count -gt 3) { Fail-Usage 'one diff file and at most two budgets' }
$diff = [string]$args[0]
if (-not (Test-Path -LiteralPath $diff -PathType Leaf)) { Fail-Usage "cannot read $diff" }
$budgetText = @('400', '15')
for ($i = 1; $i -lt $args.Count; $i++) { $budgetText[$i - 1] = [string]$args[$i] }
foreach ($n in $budgetText) {
    if ($n -notmatch '^(0|[1-9][0-9]*)$') { Fail-Usage "budget $n is not a whole number" }
}
$budgetLines = [long]$budgetText[0]
$budgetFiles = [long]$budgetText[1]

# The patterns are case-sensitive, as awk's are, except the assignment pattern, which facts.sh matches lowercased.
$secretPatterns = @(
    '-----BEGIN [A-Z ]*PRIVATE KEY-----',
    'ghp_[A-Za-z0-9]{36}',
    'AKIA[0-9A-Z]{16}',
    'github_pat_[A-Za-z0-9_]{20}'
)
$assignment = '(api_?key|secret|token|passw(or)?d)["'']?[ \t]*[:=]+[ \t]*["''][^"'']{8}'
$sleep = 'time\.Sleep\('
$skip = '\.Skip(f|Now)?\(|SkipUnlessRequired\('
$assertion = '(^|[^A-Za-z0-9_])(t|b|tb|f)\.((Error|Errorf|Fatal|Fatalf)\([^)]|(Fail|FailNow)\(\))'

$facts = [ordered]@{
    files = 0; test_files = 0; nontest_lines = 0; sleeps_added = 0; sleeps_removed = 0
    skips_added = 0; skips_removed = 0; assertions_removed = 0; secrets = 0; workflow_problems = 0
}
$items = [System.Collections.Generic.List[string]]::new()
$path = ''; $test = $false; $go = $false; $workflow = $false
$oldn = 0; $newn = 0; $oldLeft = 0; $newLeft = 0

function Set-DiffPath([string]$p) {
    $script:path = $p
    $script:test = ($p -cmatch '_test\.go$') -or ($p -cmatch '(^|/)testdata/')
    $script:go = $p -cmatch '\.go$'
    $script:workflow = $p -cmatch '^\.github/workflows/'
}
function Add-Item([string]$kind, [long]$line) { $items.Add("$kind ${path}:$line") }
function Test-Secret([string]$s) {
    foreach ($p in $secretPatterns) { if ($s -cmatch $p) { return $true } }
    return $s.ToLowerInvariant() -cmatch $assignment
}
function Test-Unpinned([string]$s) {
    if ($s -cnotmatch '^[ \t-]*uses:') { return $false }
    $ref = $s -creplace '^[ \t-]*uses:[ \t]*', ''
    $ref = $ref -creplace '[ \t]+#.*$', ''
    $ref = $ref -creplace '["'']', ''
    if ($ref -cmatch '^\./') { return $false }
    if ($ref -cmatch '^docker://') { return $ref -cnotmatch '@sha256:' }
    return $ref -cnotmatch '@[0-9a-f]{40}$'
}

foreach ($raw in [System.IO.File]::ReadAllLines((Resolve-Path -LiteralPath $diff).ProviderPath)) {
    $line = $raw -creplace "`r$", ''
    if ($oldLeft -gt 0 -or $newLeft -gt 0) {
        $c = if ($line.Length -gt 0) { $line.Substring(0, 1) } else { '' }
        $s = if ($line.Length -gt 1) { $line.Substring(1) } else { '' }
        if ($c -eq '-') {
            if ($go -and $test) {
                if ($s -cmatch $sleep) { $facts.sleeps_removed++ }
                if ($s -cmatch $skip) { $facts.skips_removed++ }
                if ($s -cmatch $assertion) { $facts.assertions_removed++; Add-Item 'assertion-removed' $oldn }
            }
            $oldn++; $oldLeft--
            if (-not $test) { $facts.nontest_lines++ }
        } elseif ($c -eq '+') {
            if (Test-Secret $s) { $facts.secrets++; Add-Item 'secret' $newn }
            if ($go -and $test) {
                if ($s -cmatch $sleep) { $facts.sleeps_added++; Add-Item 'sleep-added' $newn }
                if ($s -cmatch $skip) { $facts.skips_added++; Add-Item 'skip-added' $newn }
            }
            if ($workflow) {
                if ($s -cmatch 'pull_request_target') { $facts.workflow_problems++; Add-Item 'pull-request-target' $newn }
                if ($s -cmatch '^[ \t]*[a-z-]+:[ \t]*write([ \t]|$)' -or $s -cmatch 'write-all') {
                    $facts.workflow_problems++; Add-Item 'write-permission' $newn
                }
                if (Test-Unpinned $s) { $facts.workflow_problems++; Add-Item 'unpinned-action' $newn }
            }
            $newn++; $newLeft--
            if (-not $test) { $facts.nontest_lines++ }
        } elseif ($c -eq ' ') {
            $oldn++; $newn++; $oldLeft--; $newLeft--
        }
        continue
    }
    if ($line.StartsWith('diff --git ')) {
        $i = $line.LastIndexOf(' b/')
        Set-DiffPath $line.Substring($i + 3)
        $facts.files++
        if ($test) { $facts.test_files++ }
    } elseif ($line.StartsWith('+++ b/')) {
        Set-DiffPath $line.Substring(6)
    } elseif ($line -cmatch '^@@ -([0-9]+)(,([0-9]+))? \+([0-9]+)(,([0-9]+))? @@') {
        $oldn = [long]$Matches[1]; $oldLeft = if ($Matches[3]) { [long]$Matches[3] } else { 1 }
        $newn = [long]$Matches[4]; $newLeft = if ($Matches[6]) { [long]$Matches[6] } else { 1 }
    }
}

$over = ($budgetLines -gt 0 -and $facts.nontest_lines -gt $budgetLines) -or ($budgetFiles -gt 0 -and $facts.files -gt $budgetFiles)
$out = [System.Text.StringBuilder]::new()
foreach ($k in 'files', 'test_files', 'nontest_lines') { [void]$out.Append("${k}: $($facts[$k])`n") }
[void]$out.Append("size_budget_lines: $budgetLines`nsize_budget_files: $budgetFiles`n")
[void]$out.Append("size_over: $(if ($over) { 'yes' } else { 'no' })`n")
foreach ($k in 'sleeps_added', 'sleeps_removed', 'skips_added', 'skips_removed', 'assertions_removed', 'secrets', 'workflow_problems') {
    [void]$out.Append("${k}: $($facts[$k])`n")
}
foreach ($it in $items) { [void]$out.Append("$it`n") }
[Console]::Out.Write($out.ToString())
exit 0
