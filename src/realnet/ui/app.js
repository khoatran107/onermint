"use strict";

const encoder = new TextEncoder();
const walletStorageKey = "lab01-ed25519-wallet-v1";
let wallet;

function bytesToHex(bytes) {
  return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
}

function base64urlToBytes(value) {
  const padded = value.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((value.length + 3) % 4);
  return Uint8Array.from(atob(padded), c => c.charCodeAt(0));
}

function bytesToBase64(bytes) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function u32(value) {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, value, false);
  return out;
}

function u64(value) {
  const out = new Uint8Array(8);
  new DataView(out.buffer).setBigUint64(0, BigInt(value), false);
  return out;
}

function join(...parts) {
  const out = new Uint8Array(parts.reduce((size, part) => size + part.length, 0));
  let offset = 0;
  for (const part of parts) { out.set(part, offset); offset += part.length; }
  return out;
}

function blob(bytes) { return join(u32(bytes.length), bytes); }

// Must remain byte-identical to crypto.CanonicalTxBytes in Go.
function canonicalTxBytes(tx) {
  const context = encoder.encode(`TX:${tx.ChainID}`);
  return join(
    Uint8Array.of(1), blob(context), Uint8Array.from(tx.Sender),
    blob(encoder.encode(tx.TargetKey)), blob(tx.Value), u64(tx.Nonce),
  );
}

async function loadWallet(forceNew = false) {
  let jwk = forceNew ? null : JSON.parse(localStorage.getItem(walletStorageKey) || "null");
  if (!jwk) {
    const generated = await crypto.subtle.generateKey({name: "Ed25519"}, true, ["sign", "verify"]);
    jwk = await crypto.subtle.exportKey("jwk", generated.privateKey);
    localStorage.setItem(walletStorageKey, JSON.stringify(jwk));
  }
  const privateKey = await crypto.subtle.importKey("jwk", jwk, {name: "Ed25519"}, false, ["sign"]);
  const publicBytes = base64urlToBytes(jwk.x);
  wallet = {privateKey, publicBytes, publicHex: bytesToHex(publicBytes)};
  document.querySelector("#owner").textContent = wallet.publicHex;
}

function rpcURL() {
  const raw = document.querySelector("#nodeUrl").value.trim() || location.origin;
  return raw.replace(/\/$/, "") + "/rpc";
}

let requestID = 0;
async function rpc(method, params = {}) {
  const response = await fetch(rpcURL(), {
    method: "POST", headers: {"Content-Type": "application/json"},
    body: JSON.stringify({jsonrpc: "2.0", id: ++requestID, method, params}),
  });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const body = await response.json();
  if (body.error) throw new Error(`${body.error.code}: ${body.error.message}`);
  return body.result;
}

async function sendTransaction() {
  const chainID = await rpc("lab_chainId");
  const account = await rpc("lab_getAccount", {sender: wallet.publicHex});
  const record = document.querySelector("#record").value.trim();
  if (!record || record.includes("/")) throw new Error("Key phải khác rỗng và không chứa '/'.");
  const value = encoder.encode(document.querySelector("#value").value);
  const tx = {
    ChainID: chainID, Sender: Array.from(wallet.publicBytes),
    TargetKey: `${wallet.publicHex}/${record}`, Value: value,
    Nonce: account.nextNonce, Signature: [],
  };
  tx.Signature = Array.from(new Uint8Array(await crypto.subtle.sign("Ed25519", wallet.privateKey, canonicalTxBytes(tx))));
  const wire = {...tx, Value: bytesToBase64(value)};
  const transactionID = await rpc("lab_sendRawTransaction", {transaction: wire});
  return {transactionID, nonce: tx.Nonce, targetKey: tx.TargetKey, node: rpcURL()};
}

async function run(button, action) {
  const output = document.querySelector("#output");
  button.disabled = true;
  try {
    const result = await action();
    output.textContent = typeof result === "string" ? result : JSON.stringify(result, null, 2);
  }
  catch (error) { output.textContent = `Lỗi: ${error.message}`; }
  finally { button.disabled = false; }
}

async function statusText() {
  const status = await rpc("lab_status");
  const lock = status.lock ? `round ${status.lock.round}, ${status.lock.blockHash}` : "không có";
  return [
    `Node: ${status.nodeId}  |  Chain: ${status.chainId}`,
    `Block đã final: ${status.finalizedHeight}`,
    `Đang voting: height ${status.consensusHeight}, round ${status.round}, bước ${status.step}`,
    `Mempool: ${status.mempoolTransactions} transaction đang chờ`,
    `Lock: ${lock}`,
  ].join("\n");
}

if (typeof module !== "undefined") module.exports = {canonicalTxBytes, bytesToHex};

if (typeof document !== "undefined") {
  document.querySelector("#nodeUrl").value = location.origin;
  document.querySelector("#send").addEventListener("click", event => run(event.currentTarget, sendTransaction));
  document.querySelector("#status").addEventListener("click", event => run(event.currentTarget, statusText));
  document.querySelector("#newWallet").addEventListener("click", event => run(event.currentTarget, async () => {
    await loadWallet(true); return {owner: wallet.publicHex, note: "Đã tạo ví mới; nonce bắt đầu lại từ 1."};
  }));
  loadWallet().catch(error => { document.querySelector("#output").textContent = `Không tạo được ví Ed25519: ${error.message}`; });
}
