"use strict";
// Scripted client: tự tay viết từng transaction trong main() rồi chạy
//
//   node tests/scripted_client.js
//   NODE_URL=http://192.168.1.10:31003 node tests/scripted_client.js
//
// Cần Node >= 20 (WebCrypto Ed25519, fetch có sẵn). Không cần browser.

const fs = require("node:fs");
const path = require("node:path");

const encoder = new TextEncoder();
const DEFAULT_NODE_URL = "http://127.0.0.1:31000";
const WALLET_DIR = path.join(__dirname, "wallets"); // mỗi ví một file JWK, chứa private key (test-only)

// ---------------------------------------------------------------- encoding
function bytesToHex(bytes) {
  return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
}

function base64urlToBytes(value) {
  return new Uint8Array(Buffer.from(value, "base64url"));
}

function bytesToBase64(bytes) {
  return Buffer.from(bytes).toString("base64");
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

// ------------------------------------------------------------------ wallets
// wallet("alice") tạo ví lần đầu rồi lưu vào tests/wallets/alice.json; các lần
// sau dùng lại. Mỗi tên là một ví (một Sender) khác nhau.
async function wallet(name) {
  if (!/^[\w-]+$/.test(name)) throw new Error("tên ví chỉ gồm chữ, số, '_' và '-'");
  const file = path.join(WALLET_DIR, `${name}.json`);
  let jwk;
  if (fs.existsSync(file)) {
    jwk = JSON.parse(fs.readFileSync(file, "utf8"));
  } else {
    const generated = await crypto.subtle.generateKey({name: "Ed25519"}, true, ["sign", "verify"]);
    jwk = await crypto.subtle.exportKey("jwk", generated.privateKey);
    fs.mkdirSync(WALLET_DIR, {recursive: true});
    fs.writeFileSync(file, JSON.stringify(jwk), {mode: 0o600});
  }
  const privateKey = await crypto.subtle.importKey("jwk", jwk, {name: "Ed25519"}, false, ["sign"]);
  const publicBytes = base64urlToBytes(jwk.x);
  return {name, privateKey, publicBytes, publicHex: bytesToHex(publicBytes)};
}

// --------------------------------------------------------------------- RPC
let requestID = 0;
async function rpc(nodeUrl, method, params = {}) {
  const url = nodeUrl.replace(/\/+$/, "") + "/rpc";
  let response;
  try {
    response = await fetch(url, {
      method: "POST", headers: {"Content-Type": "application/json"},
      body: JSON.stringify({jsonrpc: "2.0", id: ++requestID, method, params}),
    });
  } catch (error) {
    throw new Error(`Không kết nối được tới ${url}: ${error.cause?.code || error.message}`);
  }
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  const body = await response.json();
  if (body.error) throw new Error(`${body.error.code}: ${body.error.message}`);
  return body.result;
}

// ------------------------------------------------------------ transactions
// spec:
//   key     phần sau '/' của TargetKey (bắt buộc, không chứa '/')
//   value   chuỗi (hoặc Uint8Array) ghi vào key
//   nonce   (tuỳ chọn) ép nonce; mặc định lấy nextNonce từ node
//   node    (tuỳ chọn) URL node nhận tx; mặc định NODE_URL
//   mutate  (tuỳ chọn) hàm (tx) => void, sửa tx SAU KHI ký (ví dụ làm hỏng chữ ký)
//   dryRun  (tuỳ chọn) true: chỉ ký và in tx, không gửi
async function send(w, spec) {
  const node = spec.node || process.env.NODE_URL || DEFAULT_NODE_URL;
  if (!spec.key || spec.key.includes("/")) throw new Error("key phải khác rỗng và không chứa '/'");
  const chainID = await rpc(node, "lab_chainId");
  const nonce = spec.nonce ?? (await rpc(node, "lab_getAccount", {sender: w.publicHex})).nextNonce;
  const value = typeof spec.value === "string" ? encoder.encode(spec.value) : Uint8Array.from(spec.value ?? []);
  const tx = {
    ChainID: chainID, Sender: Array.from(w.publicBytes),
    TargetKey: `${w.publicHex}/${spec.key}`, Value: value,
    Nonce: nonce, Signature: [],
  };
  tx.Signature = Array.from(new Uint8Array(await crypto.subtle.sign("Ed25519", w.privateKey, canonicalTxBytes(tx))));
  if (spec.mutate) spec.mutate(tx);
  const wire = {...tx, Value: bytesToBase64(tx.Value)};
  if (spec.dryRun) return {dryRun: true, node, wire};
  const transactionID = await rpc(node, "lab_sendRawTransaction", {transaction: wire});
  return {transactionID, nonce, targetKey: tx.TargetKey, node};
}

async function status(nodeUrl = process.env.NODE_URL || DEFAULT_NODE_URL) {
  return rpc(nodeUrl, "lab_status");
}

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));

// Gửi, in kết quả; lỗi của một tx không dừng cả script (để thử tx cố tình sai).
async function attempt(label, w, spec) {
  try {
    console.log(`[${label}]`, JSON.stringify(await send(w, spec)));
  } catch (error) {
    console.log(`[${label}] LỖI: ${error.message}`);
  }
}

// ======================================================================
// VIẾT TỪNG TRANSACTION Ở ĐÂY
// ======================================================================
async function main() {
  const alice = await wallet("alice");
  const bob = await wallet("bob");
  console.log("alice:", alice.publicHex);
  console.log("bob:  ", bob.publicHex);

  for (let i = 0; i < 100; i++) {
    await attempt("tx1", alice, {key: "message", value: "hello blockchain"});
  }

}

if (typeof module !== "undefined") module.exports = {wallet, send, status, rpc, attempt, sleep, canonicalTxBytes, bytesToHex};

if (require.main === module) {
  main().catch(error => { console.error(`Lỗi: ${error.message}`); process.exitCode = 1; });
}
