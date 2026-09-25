# Device Report Proxy Worker

This document describes the Cloudflare Worker that handles device diagnostic reports for PrintMaster.

## Overview

The proxy worker receives diagnostic reports from PrintMaster agents, creates a GitHub Gist with the full diagnostic data, and returns URLs for creating a pre-filled GitHub issue.

## Endpoint

**URL**: `https://api.printmaster.work/diagnostic`  
**Method**: `POST`  
**Content-Type**: `application/json`

## Request Format

```json
{
  "report": {
    "report_id": "RPT-18B2A3C4D5E6-1234",
    "timestamp": "2024-01-15T10:30:00Z",
    "agent_version": "0.25.7",
    "os": "windows",
    "arch": "amd64",
    "issue_type": "wrong_manufacturer",
    "expected_value": "HP LaserJet Pro M404dn",
    "user_message": "Device shows as Unknown instead of HP",
    "device_ip": "10.0.1.50",
    "device_serial": "VNC1234567",
    "device_model": "HP LaserJet Pro M404dn",
    "device_mac": "00:11:22:33:44:55",
    "current_manufacturer": "Unknown",
    "current_model": "Unknown Printer",
    "current_serial": "VNC1234567",
    "current_hostname": "[hash:a1b2c3d4]",
    "current_page_count": 15234,
    "detected_vendor": "Unknown",
    "detection_steps": [
      "Querying sysDescr (.1.3.6.1.2.1.1.1.0)",
      "No vendor match found"
    ],
    "snmp_responses": [
      {
        "oid": ".1.3.6.1.2.1.1.1.0",
        "type": "OctetString",
        "value": "HP LaserJet Pro M404dn",
        "hex_value": ""
      }
    ],
    "recent_logs": [
      "2024-01-15T10:29:55Z [INFO] Scanning device 10.0.1.50"
    ]
  }
}
```

## Response Format

### Success (200 OK)

```json
{
  "success": true,
  "gist_url": "https://gist.github.com/printmaster-bot/abc123def456",
  "issue_url": "https://github.com/printmaster-org/printmaster/issues/new?template=device-report.yml&title=%5BDevice+Report%5D+wrong_manufacturer+%E2%80%93+HP+LaserJet+Pro+M404dn&gist_url=https%3A%2F%2Fgist.github.com%2Fprintmaster-bot%2Fabc123def456&issue_type=Wrong+manufacturer+detection&expected_value=HP+LaserJet+Pro+M404dn&device_model=HP+LaserJet+Pro+M404dn&device_manufacturer=Unknown"
}
```

### Error (4xx/5xx)

```json
{
  "success": false,
  "error": "Failed to create gist: rate limit exceeded"
}
```

## Cloudflare Worker Implementation

Create a new Cloudflare Worker with the following code:

```javascript
// wrangler.toml
// name = "printmaster-diagnostic-proxy"
// main = "src/worker.js"
// compatibility_date = "2024-01-01"
// 
// [vars]
// GITHUB_REPO = "printmaster-org/printmaster"
//
// [secrets]
// GITHUB_PAT = "ghp_..." (set via wrangler secret put)

export default {
  async fetch(request, env) {
    // Handle CORS preflight
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        headers: {
          'Access-Control-Allow-Origin': '*',
          'Access-Control-Allow-Methods': 'POST, OPTIONS',
          'Access-Control-Allow-Headers': 'Content-Type',
          'Access-Control-Max-Age': '86400',
        },
      });
    }

    // Only allow POST to /diagnostic
    const url = new URL(request.url);
    if (url.pathname !== '/diagnostic' || request.method !== 'POST') {
      return new Response(JSON.stringify({ success: false, error: 'Not found' }), {
        status: 404,
        headers: { 'Content-Type': 'application/json' },
      });
    }

    try {
      const body = await request.json();
      const report = body.report;

      if (!report || !report.report_id) {
        return new Response(JSON.stringify({ success: false, error: 'Invalid report format' }), {
          status: 400,
          headers: corsHeaders('application/json'),
        });
      }

      // Create Gist with diagnostic data
      const gistResponse = await fetch('https://api.github.com/gists', {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${env.GITHUB_PAT}`,
          'Accept': 'application/vnd.github+json',
          'User-Agent': 'PrintMaster-Diagnostic-Proxy/1.0',
          'X-GitHub-Api-Version': '2022-11-28',
        },
        body: JSON.stringify({
          description: `PrintMaster Device Report: ${report.issue_type} - ${report.device_model || 'Unknown'}`,
          public: false,
          files: {
            'diagnostic_report.json': {
              content: JSON.stringify(report, null, 2),
            },
            'summary.md': {
              content: generateSummary(report),
            },
          },
        }),
      });

      if (!gistResponse.ok) {
        const errorText = await gistResponse.text();
        console.error('GitHub API error:', errorText);
        return new Response(JSON.stringify({ 
          success: false, 
          error: `Failed to create gist: ${gistResponse.status}` 
        }), {
          status: 502,
          headers: corsHeaders('application/json'),
        });
      }

      const gist = await gistResponse.json();
      const gistUrl = gist.html_url;

      // Build pre-filled issue URL
      const issueUrl = buildIssueUrl(env.GITHUB_REPO, report, gistUrl);

      return new Response(JSON.stringify({
        success: true,
        gist_url: gistUrl,
        issue_url: issueUrl,
      }), {
        status: 200,
        headers: corsHeaders('application/json'),
      });

    } catch (error) {
      console.error('Worker error:', error);
      return new Response(JSON.stringify({ 
        success: false, 
        error: error.message 
      }), {
        status: 500,
        headers: corsHeaders('application/json'),
      });
    }
  },
};

function corsHeaders(contentType) {
  return {
    'Content-Type': contentType,
    'Access-Control-Allow-Origin': '*',
  };
}

function generateSummary(report) {
  const issueTypeLabels = {
    'wrong_manufacturer': 'Wrong Manufacturer Detection',
    'wrong_model': 'Wrong Model Detection',
    'missing_serial': 'Missing Serial Number',
    'wrong_serial': 'Wrong Serial Number',
    'incorrect_counters': 'Incorrect Page Counters',
    'missing_toner': 'Missing Toner/Ink Levels',
    'missing_supplies': 'Missing Supplies Data',
    'wrong_hostname': 'Wrong Hostname',
    'other': 'Other Issue',
  };

  return `# Device Data Report

## Issue Type
${issueTypeLabels[report.issue_type] || report.issue_type}

## Expected Value
${report.expected_value || 'Not specified'}

## User Message
${report.user_message || 'No additional message'}

## Current Detection Results
- **Manufacturer**: ${report.current_manufacturer || 'Unknown'}
- **Model**: ${report.current_model || 'Unknown'}
- **Serial**: ${report.current_serial || 'Unknown'}
- **Page Count**: ${report.current_page_count || 'N/A'}

## Device Info
- **IP**: ${report.device_ip || 'Unknown'}
- **MAC**: ${report.device_mac || 'Unknown'}
- **Detected Vendor**: ${report.detected_vendor || 'Unknown'}

## Agent Info
- **Version**: ${report.agent_version || 'Unknown'}
- **OS**: ${report.os || 'Unknown'}
- **Arch**: ${report.arch || 'Unknown'}

## Detection Steps
${(report.detection_steps || []).map(s => '- ' + s).join('\n') || 'No steps recorded'}

## SNMP Responses
\`\`\`json
${JSON.stringify(report.snmp_responses || [], null, 2)}
\`\`\`

---
*Report ID: ${report.report_id}*  
*Timestamp: ${report.timestamp}*
`;
}

function buildIssueUrl(repo, report, gistUrl) {
  const issueTypeLabels = {
    'wrong_manufacturer': 'Wrong manufacturer detection',
    'wrong_model': 'Wrong model detection',
    'missing_serial': 'Missing serial number',
    'wrong_serial': 'Wrong serial number',
    'incorrect_counters': 'Incorrect page counters',
    'missing_toner': 'Missing toner/ink levels',
    'missing_supplies': 'Missing supplies data',
    'wrong_hostname': 'Wrong hostname',
    'other': 'Other',
  };

  const title = `[Device Report] ${report.issue_type} – ${report.device_model || 'Unknown Device'}`;
  
  const params = new URLSearchParams({
    template: 'device-report.yml',
    title: title,
    gist_url: gistUrl,
    issue_type: issueTypeLabels[report.issue_type] || 'Other',
    expected_value: report.expected_value || '',
    device_model: report.device_model || '',
    device_manufacturer: report.current_manufacturer || '',
  });

  return `https://github.com/${repo}/issues/new?${params.toString()}`;
}
```

## Setup Instructions

1. **Create a GitHub Personal Access Token (PAT)**
   - Go to GitHub Settings > Developer Settings > Personal Access Tokens > Fine-grained tokens
   - Create a token with `gist` scope (read/write)
   - Note: Use a bot account to avoid rate limits on your personal account

2. **Create the Cloudflare Worker**
   ```bash
   npm create cloudflare@latest printmaster-diagnostic-proxy
   cd printmaster-diagnostic-proxy
   ```

3. **Configure wrangler.toml**
   ```toml
   name = "printmaster-diagnostic-proxy"
   main = "src/worker.js"
   compatibility_date = "2024-01-01"
   
   [vars]
   GITHUB_REPO = "printmaster-org/printmaster"
   ```

4. **Set the GitHub PAT secret**
   ```bash
   wrangler secret put GITHUB_PAT
   # Paste your PAT when prompted
   ```

5. **Deploy the Worker**
   ```bash
   wrangler deploy
   ```

6. **Configure Custom Domain** (optional)
   - In Cloudflare Dashboard > Workers > your worker > Settings > Domains & Routes
   - Add custom domain: `api.printmaster.work`

## Rate Limiting

The GitHub Gist API has rate limits:
- Authenticated requests: 5000/hour
- Consider implementing caching or rate limiting in the worker if needed

## Privacy Considerations

The worker:
- Does NOT log or store report data beyond the Gist
- Gists are created as **private** (unlisted) by default
- Only the user with the URL can access the Gist
- IP addresses in reports are pre-anonymized by the agent (private IPs kept, public IPs hashed)
- Hostnames with company identifiers are hashed by the agent

## Monitoring

Set up Cloudflare Worker analytics to monitor:
- Request volume
- Error rates
- Response times

## Fallback Behavior

If the proxy is unavailable:
1. Agent returns the report data to the frontend
2. Frontend downloads the report as a JSON file
3. Frontend opens GitHub issue form with manual instructions
4. User can attach the JSON file to the issue manually
