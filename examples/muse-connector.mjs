/**
 * Minimal client for the loopback-only Hermes Muse WhatsApp Connector.
 * Requires Node.js 18+ for global fetch, or pass fetchImpl explicitly.
 */
export class MuseConnectorError extends Error {
  constructor(message, { status, body, cause } = {}) {
    super(message, { cause });
    this.name = 'MuseConnectorError';
    this.status = status;
    this.body = body;
  }
}

export function isMuseChat(chatId) {
  return typeof chatId === 'string' && chatId.endsWith('@bot');
}

export function createMuseConnector({
  baseUrl = 'http://127.0.0.1:3001',
  fetchImpl = globalThis.fetch,
} = {}) {
  if (typeof fetchImpl !== 'function') throw new TypeError('fetchImpl must be a function');
  const root = baseUrl.replace(/\/$/, '');

  async function request(path, options = {}) {
    let response;
    try {
      response = await fetchImpl(`${root}${path}`, options);
    } catch (cause) {
      throw new MuseConnectorError(`Muse connector request failed: ${path}`, { cause });
    }

    let body;
    try {
      body = await response.json();
    } catch (cause) {
      throw new MuseConnectorError(`Muse connector returned non-JSON from ${path}`, {
        status: response.status,
        cause,
      });
    }
    if (!response.ok) {
      throw new MuseConnectorError(body?.error || `Muse connector returned HTTP ${response.status}`, {
        status: response.status,
        body,
      });
    }
    return body;
  }

  const drainMessages = async () => {
    const messages = await request('/messages');
    if (!Array.isArray(messages)) throw new MuseConnectorError('Muse connector returned an invalid messages payload', { body: messages });
    return messages;
  };

  return {
    health: () => request('/health'),

    async send(chatId, message) {
      if (!isMuseChat(chatId)) throw new TypeError('Muse chatId must end with @bot');
      if (typeof message !== 'string' || !message.trim()) throw new TypeError('message must be non-empty text');
      return request('/send', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ chatId, message }),
      });
    },

    drainMessages,

    /**
     * Poll sequentially. onMessage may be async. Returns a stop function.
     * The connector's /messages endpoint drains messages atomically, so do not
     * run multiple pollers against the same connector.
     */
    startPolling({ onMessage, onError = console.error, intervalMs = 1500 } = {}) {
      if (typeof onMessage !== 'function') throw new TypeError('onMessage must be a function');
      if (!Number.isFinite(intervalMs) || intervalMs < 250) throw new RangeError('intervalMs must be at least 250');
      let stopped = false;
      let timer;

      const tick = async () => {
        try {
          for (const message of await drainMessages()) await onMessage(message);
        } catch (error) {
          onError(error);
        } finally {
          if (!stopped) timer = setTimeout(tick, intervalMs);
        }
      };
      void tick();
      return () => {
        stopped = true;
        clearTimeout(timer);
      };
    },
  };
}
