(() => {
  "use strict";

  const triggerCallbacks = new Set();
  const activeEventIDs = new Set();
  let readyRequested = false;
  let readySent = false;

  const pathParts = window.location.pathname.split("/").filter(Boolean);
  const capability =
    pathParts.length === 2 && pathParts[0] === "overlay" ? pathParts[1] : "";
  if (!capability) throw new Error("OverlaySDK: invalid overlay URL");

  const socket = new WebSocket(
    `ws://${window.location.host}/ws/${encodeURIComponent(capability)}`,
  );

  const sendReady = () => {
    if (!readyRequested || readySent || socket.readyState !== WebSocket.OPEN)
      return;
    socket.send(JSON.stringify({ type: "ready", sdkVersion: "1" }));
    readySent = true;
  };

  socket.addEventListener("open", sendReady);
  socket.addEventListener("message", (message) => {
    let payload;
    try {
      payload = JSON.parse(message.data);
    } catch {
      return;
    }
    if (
      payload?.type !== "trigger" ||
      typeof payload.event !== "object" ||
      payload.event === null ||
      typeof payload.event.id !== "string"
    ) {
      return;
    }
    activeEventIDs.clear();
    activeEventIDs.add(payload.event.id);
    for (const callback of triggerCallbacks) {
      try {
        callback(payload.event);
      } catch (error) {
        window.setTimeout(() => {
          throw error;
        });
      }
    }
  });
  socket.addEventListener("close", () => activeEventIDs.clear());

  const sdk = Object.freeze({
    version: "1",
    ready() {
      readyRequested = true;
      sendReady();
    },
    onTrigger(callback) {
      if (typeof callback !== "function") {
        throw new TypeError("OverlaySDK.onTrigger requires a callback");
      }
      triggerCallbacks.add(callback);
      return () => triggerCallbacks.delete(callback);
    },
    complete(eventID) {
      if (typeof eventID !== "string" || !activeEventIDs.delete(eventID))
        return;
      if (socket.readyState !== WebSocket.OPEN) return;
      socket.send(JSON.stringify({ type: "complete", eventId: eventID }));
    },
  });

  Object.defineProperty(window, "OverlaySDK", {
    value: sdk,
    configurable: false,
    enumerable: true,
    writable: false,
  });
})();
