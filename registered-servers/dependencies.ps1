winget install Microsoft.NuGet

nuget install Microsoft.Data.SqlClient `
    -OutputDirectory (Join-Path $PSScriptRoot 'packages') `
    -ExcludeVersion
