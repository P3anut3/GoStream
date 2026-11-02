$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$targetPath = Join-Path $projectRoot '.env.compose'
if (Test-Path -LiteralPath $targetPath) {
    Write-Output '.env.compose already exists; keeping its settings.'
    exit 0
}

function New-ComposeSecret {
    $secretBytes = New-Object byte[] 32
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($secretBytes)
        return [BitConverter]::ToString($secretBytes).Replace('-', '').ToLowerInvariant()
    } finally {
        $generator.Dispose()
    }
}

$content = @"
COMPOSE_PROJECT_NAME=gostream-dev
COMPOSE_API_PORT=19080
COMPOSE_MYSQL_PORT=13307
COMPOSE_REDIS_PORT=16379
COMPOSE_AMQP_PORT=15673
COMPOSE_RABBITMQ_UI_PORT=15674

COMPOSE_MYSQL_ROOT_PASSWORD=$(New-ComposeSecret)
COMPOSE_MYSQL_PASSWORD=$(New-ComposeSecret)
COMPOSE_REDIS_PASSWORD=$(New-ComposeSecret)
COMPOSE_RABBITMQ_USER=gostream
COMPOSE_RABBITMQ_PASSWORD=$(New-ComposeSecret)
JWT_SECRET=$(New-ComposeSecret)
GOSTREAM_FEED_CURSOR_SECRET=$(New-ComposeSecret)
"@

$encoding = New-Object System.Text.UTF8Encoding -ArgumentList $false
$bytes = $encoding.GetBytes($content)
$stream = [IO.File]::Open($targetPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
try {
    $stream.Write($bytes, 0, $bytes.Length)
} finally {
    $stream.Dispose()
}
Write-Output 'Created .env.compose with local development credentials.'
