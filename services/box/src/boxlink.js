// Box side of the box<->Brain channel (mTLS). Phase-0 scope: dial the Brain presenting a
// client cert (mTLS), REGISTER with id + one-time token, then HEARTBEAT on an interval.
// No SMTP, no job stream. Uses Node's built-in https/tls only — no third-party deps.

import https from "node:https";
import fs from "node:fs";

export class BoxLink {
  /**
   * @param {object} o
   * @param {string} o.host  Brain host
   * @param {number} o.port  Brain mTLS port
   * @param {string} o.boxId
   * @param {string} o.token one-time registration token (authority)
   * @param {string} o.certPath box client cert
   * @param {string} o.keyPath  box client key
   * @param {string} o.caPath   CA that signed the Brain server cert
   */
  constructor(o) {
    this.host = o.host;
    this.port = o.port;
    this.boxId = o.boxId;
    this.token = o.token;
    // mTLS material: our client cert/key + the CA we verify the Brain against.
    this.tls = {
      cert: fs.readFileSync(o.certPath),
      key: fs.readFileSync(o.keyPath),
      ca: fs.readFileSync(o.caPath),
      // Verify the Brain's server cert against our CA; SAN is "brain"/"localhost".
      rejectUnauthorized: true,
      servername: o.serverName || "brain",
    };
  }

  #post(path, bodyObj) {
    const body = Buffer.from(JSON.stringify(bodyObj), "utf8");
    const opts = {
      host: this.host, port: this.port, path, method: "POST",
      headers: { "content-type": "application/json", "content-length": body.length },
      cert: this.tls.cert, key: this.tls.key, ca: this.tls.ca,
      rejectUnauthorized: this.tls.rejectUnauthorized, servername: this.tls.servername,
    };
    return new Promise((resolve, reject) => {
      const req = https.request(opts, (res) => {
        let data = "";
        res.on("data", (c) => (data += c));
        res.on("end", () => resolve({ status: res.statusCode, body: data }));
      });
      req.on("error", reject);
      req.write(body);
      req.end();
    });
  }

  /** Register: token is the authorization authority; the client cert is transport. */
  async register() {
    const r = await this.#post("/v1/box/register", { box_id: this.boxId, token: this.token });
    if (r.status !== 200) throw new Error(`register failed: status ${r.status}`);
    return true;
  }

  /** One heartbeat. */
  async heartbeat() {
    const r = await this.#post("/v1/box/heartbeat", { box_id: this.boxId });
    if (r.status !== 200) throw new Error(`heartbeat failed: status ${r.status}`);
    return true;
  }
}
