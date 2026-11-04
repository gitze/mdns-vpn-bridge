<template>
  <div class="mvb">
    <tlt-card title="mDNS VPN Bridge — Status">
      <p v-if="error" class="mvb-error">{{ error }}</p>
      <table v-if="status" class="mvb-table">
        <tbody>
          <tr><th>Interface</th><td>{{ status.lan_if }} ({{ status.lan_ip }})</td></tr>
          <tr><th>Peer</th><td>{{ status.peer }}:{{ status.peer_port }}</td></tr>
          <tr><th>Filter</th><td>lan2peer={{ status.lan2peer }} peer2lan={{ status.peer2lan }}</td></tr>
          <tr><th>Uptime</th><td>{{ status.uptime }}</td></tr>
          <tr><th>→ peer</th><td>{{ status.to_peer }}</td></tr>
          <tr><th>→ LAN</th><td>{{ status.to_lan }}</td></tr>
        </tbody>
      </table>
      <p v-if="!status && !error" class="mvb-muted">No status available.</p>
      <div class="mvb-actions">
        <tlt-button @click="loadStatus">Refresh</tlt-button>
      </div>
    </tlt-card>

    <tlt-card title="mDNS VPN Bridge — Configuration">
      <div class="mvb-form">
        <label class="mvb-row">
          <span>Enabled</span>
          <input type="checkbox" v-model="form.enabled" />
        </label>
        <label class="mvb-row">
          <span>LAN interface</span>
          <input type="text" v-model="form.lan_if" placeholder="br-lan" />
        </label>
        <label class="mvb-row">
          <span>Peer address (overlay)</span>
          <input type="text" v-model="form.peer" placeholder="100.x.y.z" />
        </label>
        <label class="mvb-row">
          <span>Link port</span>
          <input type="text" v-model="form.peerport" placeholder="53530" />
        </label>
        <label class="mvb-row">
          <span>LAN → peer</span>
          <select v-model="form.lan2peer">
            <option value="query">query (DNS queries only)</option>
            <option value="all">all</option>
          </select>
        </label>
        <label class="mvb-row">
          <span>Peer → LAN</span>
          <select v-model="form.peer2lan">
            <option value="all">all</option>
            <option value="query">query</option>
          </select>
        </label>
        <label class="mvb-row">
          <span>Service allowlist</span>
          <input type="text" v-model="form.services" placeholder="_http,_esphomelib,_ipp (empty = all)" />
        </label>
        <label class="mvb-row">
          <span>Stats interval</span>
          <input type="text" v-model="form.stats" placeholder="300s" />
        </label>
      </div>
      <p v-if="message" class="mvb-message">{{ message }}</p>
      <div class="mvb-actions">
        <tlt-button @click="save">Save and restart</tlt-button>
        <tlt-button @click="restart">Restart service</tlt-button>
      </div>
    </tlt-card>
  </div>
</template>

<script>
// The RutOS WebUI injects $axios and the tlt-* components globally; nothing is
// imported here. API responses are enveloped differently across firmware
// versions, so values are picked field-wise from the response object graph.
function pick(obj, key, depth = 0) {
  if (!obj || typeof obj !== "object" || depth > 4) return undefined;
  if (Object.prototype.hasOwnProperty.call(obj, key)) return obj[key];
  for (const value of Object.values(obj)) {
    const hit = pick(value, key, depth + 1);
    if (hit !== undefined) return hit;
  }
  return undefined;
}

export default {
  data() {
    return {
      status: null,
      error: "",
      message: "",
      form: {
        enabled: false,
        lan_if: "",
        peer: "",
        peerport: "53530",
        lan2peer: "all",
        peer2lan: "all",
        services: "",
        stats: "300s",
      },
    };
  },
  mounted() {
    this.loadStatus();
    this.loadConfig();
  },
  methods: {
    async loadStatus() {
      this.error = "";
      try {
        const res = await this.$axios.get("/api/mdns_vpn_bridge_f/status");
        const running = pick(res, "running");
        if (running === "0" || running === 0 || running === false) {
          this.status = null;
          this.error = "The relay is not running (service stopped or wrong peer address).";
          return;
        }
        this.status = {
          lan_if: pick(res, "lan_if") || "-",
          lan_ip: pick(res, "lan_ip") || "-",
          peer: pick(res, "peer") || "-",
          peer_port: pick(res, "peer_port") || "-",
          lan2peer: pick(res, "lan2peer") || "-",
          peer2lan: pick(res, "peer2lan") || "-",
          uptime: pick(res, "uptime") || "-",
          to_peer: pick(res, "to_peer") || "-",
          to_lan: pick(res, "to_lan") || "-",
        };
      } catch (e) {
        this.error = "Status request failed.";
      }
    },
    async loadConfig() {
      try {
        const res = await this.$axios.get("/api/mdns_vpn_bridge_f/config");
        this.form = {
          enabled: String(pick(res, "enabled")) === "1",
          lan_if: pick(res, "lan_if") || "",
          peer: pick(res, "peer") || "",
          peerport: pick(res, "peerport") || "53530",
          lan2peer: pick(res, "lan2peer") || "all",
          peer2lan: pick(res, "peer2lan") || "all",
          services: pick(res, "services") || "",
          stats: pick(res, "stats") || "300s",
        };
      } catch (e) {
        this.message = "Could not read the current configuration.";
      }
    },
    async save() {
      this.message = "";
      try {
        await this.$axios.post("/api/mdns_vpn_bridge_f/actions/set", {
          enabled: this.form.enabled ? "1" : "0",
          lan_if: this.form.lan_if,
          peer: this.form.peer,
          peerport: this.form.peerport,
          lan2peer: this.form.lan2peer,
          peer2lan: this.form.peer2lan,
          services: this.form.services,
          stats: this.form.stats,
        });
        this.message = "Configuration saved, service restarted.";
        await this.loadStatus();
      } catch (e) {
        this.message = "Saving failed — check the values (peer must be an IPv4 address).";
      }
    },
    async restart() {
      this.message = "";
      try {
        await this.$axios.post("/api/mdns_vpn_bridge_f/actions/restart");
        this.message = "Service restarted.";
        await this.loadStatus();
      } catch (e) {
        this.message = "Restart failed.";
      }
    },
  },
};
</script>

<style scoped>
.mvb-table {
  width: 100%;
  border-collapse: collapse;
  margin-bottom: 12px;
}
.mvb-table th {
  text-align: left;
  font-weight: 600;
  padding: 4px 12px 4px 0;
  white-space: nowrap;
  vertical-align: top;
}
.mvb-table td {
  padding: 4px 0;
}
.mvb-form {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-width: 520px;
}
.mvb-row {
  display: grid;
  grid-template-columns: 200px 1fr;
  align-items: center;
  gap: 8px;
}
.mvb-row input[type="text"],
.mvb-row select {
  width: 100%;
  padding: 4px 6px;
}
.mvb-actions {
  margin-top: 12px;
  display: flex;
  gap: 8px;
}
.mvb-error {
  color: #b00020;
}
.mvb-message {
  margin-top: 8px;
}
.mvb-muted {
  opacity: 0.7;
}
</style>
