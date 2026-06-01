export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const corsHeaders = {
      'Access-Control-Allow-Origin': '*',
      'Access-Control-Allow-Methods': 'GET, POST, OPTIONS',
      'Access-Control-Allow-Headers': 'Content-Type',
    };

    if (request.method === 'OPTIONS') {
      return new Response(null, { headers: corsHeaders });
    }

    if (url.pathname === '/ping' && request.method === 'POST') {
      return handlePing(request, env, corsHeaders);
    }

    if (url.pathname === '/stats' && request.method === 'GET') {
      return handleStats(request, env, corsHeaders);
    }

    return new Response('Not Found', { status: 404 });
  },
};

async function handlePing(request, env, corsHeaders) {
  try {
    const body = await request.json();
    const typ = body.type === 'npc' ? 'npc' : 'nps';
    const ip = request.headers.get('CF-Connecting-IP') || '';
    const country = request.headers.get('CF-IPCountry') || 'XX';

    const today = new Date().toISOString().slice(0, 10);
    const key = 'day:' + today;

    let records = [];
    const existing = await env.INSTALLS.get(key, 'json');
    if (existing) records = existing;

    records.push({
      t: typ,
      c: country,
      ip: ip,
      ts: Date.now(),
    });

    await env.INSTALLS.put(key, JSON.stringify(records), { expirationTtl: 365 * 86400 });

    return new Response(JSON.stringify({ ok: true }), {
      headers: { ...corsHeaders, 'Content-Type': 'application/json' },
    });
  } catch (e) {
    return new Response(JSON.stringify({ ok: false }), {
      headers: { ...corsHeaders, 'Content-Type': 'application/json' },
    });
  }
}

async function handleStats(request, env, corsHeaders) {
  try {
    const cached = await env.INSTALLS.get('stats', 'json');
    if (cached) {
      return new Response(JSON.stringify(cached), {
        headers: { ...corsHeaders, 'Content-Type': 'application/json' },
      });
    }

    const list = await env.INSTALLS.list({ prefix: 'day:' });
    const countries = {};
    let totalNps = 0;
    let totalNpc = 0;

    for (const key of list.keys) {
      const dayData = await env.INSTALLS.get(key.name, 'json');
      if (!dayData) continue;
      for (const r of dayData) {
        const code = r.c || 'XX';
        if (!countries[code]) countries[code] = { nps: 0, npc: 0 };
        if (r.t === 'npc') {
          countries[code].npc++;
          totalNpc++;
        } else {
          countries[code].nps++;
          totalNps++;
        }
      }
    }

    const stats = {
      updated_at: new Date().toISOString(),
      total_nps: totalNps,
      total_npc: totalNpc,
      countries,
    };

    await env.INSTALLS.put('stats', JSON.stringify(stats), { expirationTtl: 300 });

    return new Response(JSON.stringify(stats), {
      headers: { ...corsHeaders, 'Content-Type': 'application/json' },
    });
  } catch (e) {
    return new Response(JSON.stringify({ error: e.message }), {
      status: 500,
      headers: { ...corsHeaders, 'Content-Type': 'application/json' },
    });
  }
}
