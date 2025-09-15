# Windows Troubleshooting Guide

This guide addresses common issues when running GABS on Windows, particularly Windows 10 and Windows 11.

## Port Availability Issues

### Problem: "No available ports in range" Error

**Error Message:**
```
Failed to start rimworld: failed to create bridge config: failed to find available port: no available ports in range 49152-65535
```

**Root Cause:**
Windows 10/11 systems, especially those with Hyper-V, WSL, Docker, or certain antivirus software, may reserve large port ranges that conflict with GABS's default port allocation.

### Solutions

#### 1. Automatic Fallback (Recommended)

GABS automatically tries multiple port ranges when the default range fails:

1. **49152-65535** (Default Windows/IANA ephemeral range)
2. **32768-49151** (Linux ephemeral range)
3. **8000-8999** (Common HTTP alternate ports)
4. **9000-9999** (Common application ports)
5. **10000-19999** (Registered/dynamic range subset)
6. **20000-29999** (Registered/dynamic range subset)
7. **30000-32767** (Registered/dynamic range subset)

If you're still getting errors, proceed to the manual solutions below.

#### 2. Custom Port Ranges

Set the `GABS_PORT_RANGES` environment variable to specify custom port ranges:

**Single Range:**
```bash
set GABS_PORT_RANGES=8000-8999
gabs server
```

**Multiple Ranges:**
```bash
set GABS_PORT_RANGES=8000-8999,9000-9999,10000-10999
gabs server
```

**PowerShell:**
```powershell
$env:GABS_PORT_RANGES="8000-8999,9000-9999"
gabs server
```

#### 3. Check Windows Reserved Ports

Run this command in an Administrator Command Prompt to see what port ranges Windows has reserved:

```cmd
netsh int ipv4 show excludedportrange protocol=tcp
```

Choose port ranges that don't conflict with the excluded ranges.

#### 4. Disable Hyper-V (If Not Needed)

If you don't need Hyper-V, you can disable it to free up port ranges:

1. Open Command Prompt as Administrator
2. Run: `dism.exe /Online /Disable-Feature:Microsoft-Hyper-V-All`
3. Restart your computer

**Warning:** This will disable all Hyper-V functionality including Docker Desktop, WSL2, and Windows Sandbox.

#### 5. Configure Windows Dynamic Port Range

You can modify Windows' dynamic port range if you have administrator access:

```cmd
# Check current settings
netsh int ipv4 show dynamicport tcp

# Set a custom range (example: start at 10000, 5000 ports)
netsh int ipv4 set dynamicport tcp start=10000 num=5000

# Restart required
```

### Testing Your Configuration

After applying any solution, test GABS port allocation:

```bash
# Test with default settings
gabs games add testgame

# Test with custom ranges
set GABS_PORT_RANGES=8000-8999
gabs games add testgame2
```

## Other Windows-Specific Issues

### Antivirus Software

Some antivirus software may block GABS from binding to ports. Consider:

1. Adding GABS to your antivirus whitelist
2. Temporarily disabling real-time protection for testing
3. Checking firewall settings

### Windows Firewall

Windows Firewall might prompt for network access. Allow GABS to communicate on private networks for local game communication.

### Path Issues

Use forward slashes `/` or escaped backslashes `\\` in configuration paths:

```json
{
  "target": "C:/Program Files/Game/game.exe"
}
```

## Getting Help

If you continue to experience issues:

1. **Check the error message** - GABS provides detailed error messages with suggestions
2. **Run port diagnostics** - Use the commands above to check port availability
3. **Try custom port ranges** - Start with ranges like `8000-9999`
4. **Check system logs** - Windows Event Viewer may show additional details
5. **Open an issue** - Include your Windows version, error message, and output from `netsh int ipv4 show excludedportrange protocol=tcp`

## Environment Variables Reference

| Variable | Description | Example |
|----------|-------------|---------|
| `GABS_PORT_RANGES` | Custom port ranges (comma-separated) | `8000-8999,9000-9999` |

## Common Working Port Ranges for Windows

These ranges typically work well on Windows systems:

- `8000-8999` - HTTP alternates
- `9000-9999` - Application ports  
- `10000-10999` - Safe dynamic range
- `20000-29999` - Usually unreserved
- `30000-32767` - Pre-ephemeral range

Choose ranges that don't conflict with your specific system's reserved ports.