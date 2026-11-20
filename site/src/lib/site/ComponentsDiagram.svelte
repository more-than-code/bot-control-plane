<!-- The Deploybot components, after the overview repo's docs/images/components.svg (more-than-code/deploybot).
     Landscape from sm up, portrait below, like PitchDiagram. Colours map to the console theme tokens through the
     classes in <style>, so dark mode follows the system theme. -->
<svg class="mx-auto hidden h-auto w-full max-w-[680px] sm:block" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 680 580" role="img" aria-labelledby="components-title" aria-describedby="components-desc" font-family="Inter, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif">
  <title id="components-title">Deploybot components</title>
  <desc id="components-desc">People and AI agents reach the bot-control-plane console and MCP gateway; both go through the API, which alone writes PostgreSQL, where secrets are encrypted. On each host, bot-agent runs unprivileged and dials out to the API, so hosts open no ports, and a root runner checks the host allowlist before running Docker containers or systemd units.</desc>
  <defs>
    <marker id="components-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" class="primary"/>
    </marker>
  </defs>

  <rect class="bg" width="680" height="580" rx="20"/>

  <!-- People and AI agents -->
  <rect class="card" x="90" y="30" width="200" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="190" y="53" font-size="14" font-weight="600" text-anchor="middle">People</text>
  <text class="muted" x="190" y="72" font-size="12" text-anchor="middle">In a browser</text>
  <rect class="card" x="390" y="30" width="200" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="490" y="53" font-size="14" font-weight="600" text-anchor="middle">AI agents</text>
  <text class="muted" x="490" y="72" font-size="12" text-anchor="middle">Any MCP client, as you</text>

  <!-- bot-control-plane -->
  <rect class="plane" x="40" y="130" width="600" height="210" rx="20" stroke-width="1.5"/>
  <rect class="card" x="70" y="180" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="160" y="203" font-size="14" font-weight="600" text-anchor="middle">Console</text>
  <text class="muted" x="160" y="222" font-size="12" text-anchor="middle">Web UI</text>
  <rect class="card" x="430" y="180" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="520" y="203" font-size="14" font-weight="600" text-anchor="middle">MCP gateway</text>
  <text class="muted" x="520" y="222" font-size="12" text-anchor="middle">OAuth, plan and confirm</text>
  <rect class="card" x="250" y="260" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="340" y="283" font-size="14" font-weight="600" text-anchor="middle">API</text>
  <text class="muted" x="340" y="302" font-size="12" text-anchor="middle">Roles, catalog, audit</text>
  <rect class="card" x="470" y="260" width="150" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="545" y="283" font-size="14" font-weight="600" text-anchor="middle">PostgreSQL</text>
  <text class="muted" x="545" y="302" font-size="12" text-anchor="middle">Secrets encrypted</text>
  <!-- The plane's name sits bottom left, where no arrow runs. -->
  <text class="primary" x="64" y="315" font-size="14" font-weight="600">bot-control-plane</text>
  <text class="muted" x="64" y="332" font-size="12">Hosted or self-hosted, AGPL-3.0</text>

  <path class="line" d="M190 86 L168 176" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <path class="line" d="M490 86 L512 176" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <path class="line" d="M160 236 L160 288 L246 288" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <path class="line" d="M520 236 L520 248 L340 248 L340 256" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <path class="line" d="M430 288 L466 288" stroke-width="1.5" marker-end="url(#components-arrow)"/>

  <!-- Your hosts -->
  <rect class="host" x="40" y="390" width="600" height="160" rx="20" stroke-width="1.5" stroke-dasharray="6 5"/>
  <text class="ink" x="620" y="414" font-size="14" font-weight="600" text-anchor="end">Your hosts</text>
  <text class="muted" x="620" y="431" font-size="12" text-anchor="end">Pi, mini PC, VPS, cloud VM</text>
  <rect class="card" x="64" y="460" width="170" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="149" y="483" font-size="14" font-weight="600" text-anchor="middle">bot-agent</text>
  <text class="muted" x="149" y="502" font-size="12" text-anchor="middle">Unprivileged, Apache-2.0</text>
  <rect class="card" x="262" y="460" width="170" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="347" y="483" font-size="14" font-weight="600" text-anchor="middle">Root runner</text>
  <text class="muted" x="347" y="502" font-size="12" text-anchor="middle">Checks host allowlist</text>
  <rect class="card" x="460" y="460" width="160" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="540" y="483" font-size="14" font-weight="600" text-anchor="middle">Docker, systemd</text>
  <text class="muted" x="540" y="502" font-size="12" text-anchor="middle">Runs your entries</text>

  <path class="line" d="M149 458 L149 370 L340 370 L340 322" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <text class="muted" x="160" y="360" font-size="12">Dials out, no open ports</text>
  <path class="line" d="M234 488 L258 488" stroke-width="1.5" marker-end="url(#components-arrow)"/>
  <path class="line" d="M432 488 L456 488" stroke-width="1.5" marker-end="url(#components-arrow)"/>
</svg>
<svg class="mx-auto block h-auto w-full max-w-[420px] sm:hidden" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 360 736" role="img" aria-labelledby="components-portrait-title" aria-describedby="components-portrait-desc" font-family="Inter, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', sans-serif">
  <title id="components-portrait-title">Deploybot components</title>
  <desc id="components-portrait-desc">People and AI agents reach the bot-control-plane console and MCP gateway; both go through the API, which alone writes PostgreSQL, where secrets are encrypted. On each host, bot-agent runs unprivileged and dials out to the API, so hosts open no ports, and a root runner checks the host allowlist before running Docker containers or systemd units.</desc>
  <defs>
    <marker id="components-portrait-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" class="primary"/>
    </marker>
  </defs>

  <rect class="bg" width="360" height="736" rx="20"/>

  <!-- People and AI agents, side by side -->
  <rect class="card" x="22" y="20" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="96" y="43" font-size="14" font-weight="600" text-anchor="middle">People</text>
  <text class="muted" x="96" y="62" font-size="12" text-anchor="middle">In a browser</text>
  <rect class="card" x="190" y="20" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="264" y="43" font-size="14" font-weight="600" text-anchor="middle">AI agents</text>
  <text class="muted" x="264" y="62" font-size="12" text-anchor="middle">Any MCP client, as you</text>

  <!-- bot-control-plane, its name at the bottom so no arrow crosses it -->
  <rect class="plane" x="12" y="110" width="336" height="258" rx="20" stroke-width="1.5"/>
  <rect class="card" x="22" y="168" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="96" y="191" font-size="14" font-weight="600" text-anchor="middle">Console</text>
  <text class="muted" x="96" y="210" font-size="12" text-anchor="middle">Web UI</text>
  <rect class="card" x="190" y="168" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="264" y="191" font-size="14" font-weight="600" text-anchor="middle">MCP gateway</text>
  <text class="muted" x="264" y="210" font-size="12" text-anchor="middle">OAuth, plan and confirm</text>
  <rect class="card" x="22" y="262" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="96" y="285" font-size="14" font-weight="600" text-anchor="middle">API</text>
  <text class="muted" x="96" y="304" font-size="12" text-anchor="middle">Roles, catalog, audit</text>
  <rect class="card" x="190" y="262" width="148" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="264" y="285" font-size="14" font-weight="600" text-anchor="middle">PostgreSQL</text>
  <text class="muted" x="264" y="304" font-size="12" text-anchor="middle">Secrets encrypted</text>
  <text class="primary" x="338" y="342" font-size="14" font-weight="600" text-anchor="end">bot-control-plane</text>
  <text class="muted" x="338" y="359" font-size="12" text-anchor="end">Hosted or self-hosted, AGPL-3.0</text>

  <path class="line" d="M96 76 L96 164" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <path class="line" d="M264 76 L264 164" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <path class="line" d="M70 224 L70 258" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <path class="line" d="M264 224 L264 242 L122 242 L122 258" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <path class="line" d="M170 290 L186 290" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>

  <!-- Your hosts -->
  <rect class="host" x="12" y="420" width="336" height="296" rx="20" stroke-width="1.5" stroke-dasharray="6 5"/>
  <text class="ink" x="338" y="444" font-size="14" font-weight="600" text-anchor="end">Your hosts</text>
  <text class="muted" x="338" y="461" font-size="12" text-anchor="end">Pi, mini PC, VPS, cloud VM</text>
  <rect class="card" x="22" y="478" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="112" y="501" font-size="14" font-weight="600" text-anchor="middle">bot-agent</text>
  <text class="muted" x="112" y="520" font-size="12" text-anchor="middle">Unprivileged, Apache-2.0</text>
  <rect class="card" x="22" y="558" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="112" y="581" font-size="14" font-weight="600" text-anchor="middle">Root runner</text>
  <text class="muted" x="112" y="600" font-size="12" text-anchor="middle">Checks host allowlist</text>
  <rect class="card" x="22" y="638" width="180" height="56" rx="10" stroke-width="1.5"/>
  <text class="ink" x="112" y="661" font-size="14" font-weight="600" text-anchor="middle">Docker, systemd</text>
  <text class="muted" x="112" y="680" font-size="12" text-anchor="middle">Runs your entries</text>

  <path class="line" d="M96 476 L96 324" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <text class="muted" x="106" y="398" font-size="12">Dials out, no open ports</text>
  <path class="line" d="M112 534 L112 554" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
  <path class="line" d="M112 614 L112 634" stroke-width="1.5" marker-end="url(#components-portrait-arrow)"/>
</svg>

<style>
	svg { font-family: inherit; }
	.bg { fill: var(--card); }
	.card { fill: var(--background); stroke: var(--border); }
	.plane { fill: var(--muted); stroke: var(--primary); }
	.host { fill: var(--muted); stroke: var(--muted-foreground); }
	.ink { fill: var(--foreground); }
	.muted { fill: var(--muted-foreground); }
	.primary { fill: var(--primary); }
	.line { stroke: var(--primary); fill: none; }
</style>
