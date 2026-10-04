import assert from 'node:assert/strict';
import test from 'node:test';
import { createMuseConnector, isMuseChat, MuseConnectorError } from './muse-connector.mjs';

function jsonResponse(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}

test('identifies only @bot JIDs as Muse chats', () => {
  assert.equal(isMuseChat('123@bot'), true);
  assert.equal(isMuseChat('123@s.whatsapp.net'), false);
  assert.equal(isMuseChat(''), false);
});

test('sends a Muse message using the documented endpoint and body', async () => {
  const calls = [];
  const client = createMuseConnector({
    fetchImpl: async (url, options) => {
      calls.push({ url, options });
      return jsonResponse({ success: true, messageId: 'message-1' });
    },
  });

  const result = await client.send('123@bot', 'Hello');
  assert.deepEqual(result, { success: true, messageId: 'message-1' });
  assert.equal(calls[0].url, 'http://127.0.0.1:3001/send');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].options.body), { chatId: '123@bot', message: 'Hello' });
});

test('rejects invalid send input before making a request', async () => {
  const client = createMuseConnector({ fetchImpl: async () => assert.fail('fetch should not run') });
  await assert.rejects(client.send('123@s.whatsapp.net', 'Hello'), /must end with @bot/);
  await assert.rejects(client.send('123@bot', '   '), /non-empty/);
});

test('returns the drained inbound messages array', async () => {
  const inbound = [{ chatId: '123@bot', messageId: 'reply-1', text: 'Hi' }];
  const client = createMuseConnector({ fetchImpl: async () => jsonResponse(inbound) });
  assert.deepEqual(await client.drainMessages(), inbound);
});

test('exposes connector HTTP errors with status and body', async () => {
  const client = createMuseConnector({
    fetchImpl: async () => jsonResponse({ success: false, error: 'not paired' }, 503),
  });
  await assert.rejects(client.health(), (error) => {
    assert.ok(error instanceof MuseConnectorError);
    assert.equal(error.status, 503);
    assert.equal(error.message, 'not paired');
    return true;
  });
});
