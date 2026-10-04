// Adapt these two seams to your bridge; do not copy its names blindly.
// Node 18+; `sock` is an authenticated Baileys socket.
import { createMuseConnector, isMuseChat } from './muse-connector.mjs';

const muse = createMuseConnector();

/**
 * Call from the bridge's normal outbound-text path BEFORE sock.sendMessage().
 * Returns a connector receipt for Muse traffic, or null for ordinary WhatsApp.
 */
export async function sendText(sock, chatId, text) {
  if (isMuseChat(chatId)) return muse.send(chatId, text);
  return sock.sendMessage(chatId, { text });
}

/**
 * Start exactly once when the bridge starts. `enqueueInbound` must write the
 * message to the same inbound queue/event bus consumed by the rest of your app.
 */
export function startMuseInboundBridge(enqueueInbound, reportError = console.error) {
  return muse.startPolling({
    intervalMs: 1500,
    onError: reportError,
    onMessage: async ({ chatId, messageId, sender, text, timestamp }) => {
      await enqueueInbound({
        chatId,
        messageId,
        sender,
        text,
        timestamp,
        fromMe: false,
        source: 'muse',
      });
    },
  });
}

// Example lifecycle:
// const stopMuseInboundBridge = startMuseInboundBridge((event) => incomingQueue.push(event));
// process.on('SIGTERM', () => stopMuseInboundBridge());
