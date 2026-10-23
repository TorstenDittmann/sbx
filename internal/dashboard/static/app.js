// sbx dashboard. No build step and no framework: every view is a function that
// builds DOM nodes and calls the /v1 API with the key the visitor signed in with.
(() => {
	"use strict";

	const app = document.getElementById("app");
	const state = { key: "", admin: false, cleanup: [] };

	// ---- helpers -----------------------------------------------------------

	// h builds an element. Strings become text nodes, so API data is never
	// parsed as HTML.
	function h(tag, attrs, ...children) {
		const el = document.createElement(tag);
		for (const [k, v] of Object.entries(attrs || {})) {
			if (v == null || v === false) continue;
			if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
			else if (k === "class") el.className = v;
			else if (k === "value") el.value = v;
			else el.setAttribute(k, v === true ? "" : v);
		}
		for (const c of children.flat(Infinity)) {
			if (c == null || c === false) continue;
			el.append(c instanceof Node ? c : String(c));
		}
		return el;
	}

	function toast(msg, bad) {
		const t = document.getElementById("toast");
		t.textContent = msg;
		t.className = bad ? "toast bad" : "toast";
		t.hidden = false;
		clearTimeout(toast.timer);
		toast.timer = setTimeout(() => (t.hidden = true), bad ? 5000 : 2500);
	}

	function ago(iso) {
		if (!iso) return "–";
		const d = new Date(iso);
		const s = Math.round((d.getTime() - Date.now()) / 1000);
		const abs = Math.abs(s);
		if (abs < 5) return h("span", { title: d.toLocaleString() }, "just now");
		let txt;
		if (abs < 60) txt = abs + "s";
		else if (abs < 3600) txt = Math.round(abs / 60) + "m";
		else if (abs < 86400) txt = Math.round(abs / 3600) + "h";
		else txt = Math.round(abs / 86400) + "d";
		return h("span", { title: d.toLocaleString() }, s >= 0 ? "in " + txt : txt + " ago");
	}

	function pill(status, label) {
		return h("span", { class: "pill " + status }, label || status);
	}

	async function copy(text) {
		try {
			await navigator.clipboard.writeText(text);
			toast("Copied");
		} catch {
			toast("Copy failed; select the text instead", true);
		}
	}

	function onCleanup(fn) {
		state.cleanup.push(fn);
	}

	function every(ms, fn) {
		const t = setInterval(() => {
			if (!document.hidden) fn();
		}, ms);
		onCleanup(() => clearInterval(t));
	}

	// ---- API ---------------------------------------------------------------

	class APIError extends Error {
		constructor(status, code, message) {
			super(message);
			this.status = status;
			this.code = code;
		}
	}

	async function api(method, path, body, key) {
		const headers = { Authorization: "Bearer " + (key || state.key) };
		const init = { method, headers };
		if (body !== undefined) {
			headers["Content-Type"] = "application/json";
			init.body = JSON.stringify(body);
		}
		const res = await fetch(path, init);
		if (res.status === 204) return null;
		const data = await res.json().catch(() => null);
		if (!res.ok) {
			const err = (data && data.error) || {};
			if (res.status === 401 && !key) signOut("Your key is no longer valid.");
			throw new APIError(res.status, err.code || "error", err.message || res.statusText);
		}
		return data;
	}

	function act(fn, ok) {
		return async (ev) => {
			const btn = ev && ev.currentTarget;
			if (btn) btn.disabled = true;
			try {
				// A handler returns false when the user cancelled.
				if ((await fn()) !== false && ok) toast(ok);
			} catch (e) {
				toast(e.message, true);
			} finally {
				if (btn && btn.isConnected) btn.disabled = false;
			}
		};
	}

	// ---- session -----------------------------------------------------------

	// The key lives only in memory. This origin also proxies sandbox previews
	// (/p/...), and a preview's scripts could read localStorage or
	// sessionStorage, so a reload asks for the key again.
	async function signIn(key) {
		await api("GET", "/v1/sandboxes?status=running", undefined, key);
		let admin = false;
		try {
			await api("GET", "/v1/keys", undefined, key);
			admin = true;
		} catch (e) {
			if (e.status !== 403) throw e;
		}
		state.key = key;
		state.admin = admin;
		chrome();
	}

	function signOut(msg) {
		state.key = "";
		state.admin = false;
		chrome();
		location.hash = "#/login";
		if (msg) toast(msg, true);
	}

	function chrome() {
		document.getElementById("top").hidden = !state.key;
		document.getElementById("nav-keys").hidden = !state.admin;
		document.getElementById("who").textContent = state.key
			? state.key.slice(0, 10) + "… · " + (state.admin ? "admin" : "sandbox")
			: "";
	}

	// ---- router ------------------------------------------------------------

	function route() {
		for (const fn of state.cleanup.splice(0)) {
			try {
				fn();
			} catch {}
		}
		app.replaceChildren();
		const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
		if (!state.key) return login();
		const [view, id, tab] = parts;
		for (const a of document.querySelectorAll("[data-nav]")) a.classList.toggle("on", a.dataset.nav === (view || "sandboxes"));
		if (view === "keys" && state.admin) return keys();
		if (view === "sandboxes" && id) return sandbox(id, tab || "overview");
		if (view !== "sandboxes") return void (location.hash = "#/sandboxes");
		return sandboxes();
	}

	// ---- views: login ------------------------------------------------------

	function login() {
		const input = h("input", { type: "password", placeholder: "sbx_…", autocomplete: "off", spellcheck: "false", required: true });
		const err = h("div", { class: "err" });
		const btn = h("button", { class: "primary", type: "submit" }, "Sign in");
		const form = h(
			"form",
			{ class: "card", onsubmit: async (ev) => {
				ev.preventDefault();
				err.textContent = "";
				btn.disabled = true;
				try {
					await signIn(input.value.trim());
					// Keep a deep link the visitor reloaded on.
					if (/^#?\/?(login)?$/.test(location.hash)) location.hash = "#/sandboxes";
					else route();
				} catch (e) {
					err.textContent = e.status === 401 ? "That key was not accepted." : e.message;
				} finally {
					btn.disabled = false;
				}
			} },
			h("h1", {}, "Sign in to sbx"),
			h("p", { class: "muted small" }, "Paste an API key. Admin keys can also manage keys. The key is kept in memory for this tab only, so reloading signs you out."),
			h("label", {}, "API key", input),
			err,
			btn,
		);
		app.append(h("div", { class: "login" }, form));
		input.focus();
	}

	// ---- views: sandbox list -----------------------------------------------

	function sandboxes() {
		const q = h("input", { type: "search", placeholder: "Search id, template, image, label…", class: "grow" });
		const statuses = ["", "running", "paused", "creating", "stopped", "error"];
		if (state.admin) statuses.push("warm");
		const status = h("select", {}, statuses.map((s) => h("option", { value: s }, s || "all statuses")));
		const body = h("tbody");
		const count = h("span", { class: "muted small" });
		let seq = 0;

		async function load() {
			const my = ++seq;
			const params = new URLSearchParams();
			if (q.value.trim()) params.set("q", q.value.trim());
			if (status.value) params.set("status", status.value);
			try {
				const data = await api("GET", "/v1/sandboxes?" + params);
				if (my !== seq) return;
				const list = data.sandboxes.sort((a, b) => b.created_at.localeCompare(a.created_at));
				count.textContent = list.length + (list.length === 1 ? " sandbox" : " sandboxes");
				body.replaceChildren(...(list.length ? list.map(row) : [h("tr", {}, h("td", { colspan: 7 }, h("div", { class: "empty" }, "No sandboxes match.")))]));
			} catch (e) {
				if (my === seq) toast(e.message, true);
			}
		}

		function row(sb) {
			const open = () => (location.hash = "#/sandboxes/" + encodeURIComponent(sb.id));
			const live = sb.status === "running" || sb.status === "paused";
			return h(
				"tr",
				{ class: "click", onclick: open },
				h("td", {}, h("code", {}, sb.id)),
				h("td", {}, pill(sb.status)),
				h("td", {}, sb.template || h("span", { class: "muted" }, sb.image)),
				h("td", {}, sb.backend),
				h("td", {}, sb.network),
				h("td", {}, ago(sb.created_at)),
				h(
					"td",
					{ class: "actions", onclick: (ev) => ev.stopPropagation() },
					live && h("button", { class: "sm", onclick: open }, "Open"),
					live && h("button", { class: "sm danger", onclick: act(async () => {
						if (!confirm("Delete " + sb.id + "?")) return false;
						await api("DELETE", "/v1/sandboxes/" + encodeURIComponent(sb.id));
						await load();
					}, "Deleted") }, "Delete"),
				),
			);
		}

		let debounce;
		q.addEventListener("input", () => {
			clearTimeout(debounce);
			debounce = setTimeout(load, 250);
		});
		status.addEventListener("change", load);

		const create = createForm(load);
		app.append(
			h("div", { class: "bar" }, h("h1", {}, "Sandboxes"), count, h("span", { class: "grow" }), h("button", { class: "primary", onclick: () => (create.hidden = !create.hidden) }, "New sandbox")),
			create,
			h("div", { class: "bar" }, q, status),
			h(
				"table",
				{},
				h("thead", {}, h("tr", {}, ["ID", "Status", "Template / image", "Backend", "Network", "Created", ""].map((t) => h("th", {}, t)))),
				body,
			),
		);
		load();
		every(5000, load);
	}

	function createForm(after) {
		const template = h("select", {}, h("option", { value: "" }, "server default"));
		const image = h("input", { placeholder: "or an image, e.g. alpine:3.20" });
		const ttl = h("input", { type: "number", min: 1, step: "any", value: 30, placeholder: "default", style: "width:90px" });
		const network = h("select", {}, h("option", { value: "" }, "default"), h("option", { value: "none" }, "none"), h("option", { value: "full" }, "full"));
		api("GET", "/v1/templates")
			.then((d) => template.append(...d.templates.map((t) => h("option", { value: t.name }, t.name))))
			.catch(() => {});
		const form = h(
			"form",
			{ class: "card form", hidden: true, style: "margin-bottom:16px", onsubmit: async (ev) => {
				ev.preventDefault();
				const btn = form.querySelector("button[type=submit]");
				btn.disabled = true;
				const body = {};
				// An empty TTL leaves it to the server default.
				if (ttl.value.trim()) {
					const min = Number(ttl.value);
					if (!(min > 0)) {
						toast("TTL must be a positive number of minutes", true);
						btn.disabled = false;
						return;
					}
					body.ttl_seconds = Math.max(1, Math.round(min * 60));
				}
				if (template.value) body.template = template.value;
				if (image.value.trim()) body.image = image.value.trim();
				if (network.value) body.network = { mode: network.value };
				try {
					const sb = await api("POST", "/v1/sandboxes", body);
					toast("Created " + sb.id);
					form.hidden = true;
					location.hash = "#/sandboxes/" + encodeURIComponent(sb.id);
				} catch (e) {
					toast(e.message, true);
					after();
				} finally {
					btn.disabled = false;
				}
			} },
			h("label", {}, "Template", template),
			h("label", {}, "Image", image),
			h("label", {}, "TTL (minutes)", ttl),
			h("label", {}, "Network", network),
			h("button", { class: "primary", type: "submit" }, "Create"),
		);
		return form;
	}

	// ---- views: one sandbox ------------------------------------------------

	function sandbox(id, tab) {
		const base = "/v1/sandboxes/" + encodeURIComponent(id);
		const head = h("div", { class: "bar" });
		const panel = h("div");
		const tabs = ["overview", "terminal", "logs"];
		let sb = null;

		function header() {
			const live = sb.status === "running" || sb.status === "paused";
			head.replaceChildren(...h("div", {},
				h("a", { href: "#/sandboxes" }, "Sandboxes"),
				h("span", { class: "muted" }, "/"),
				h("h1", {}, h("code", {}, sb.id)),
				pill(sb.status),
				h("span", { class: "muted small" }, live ? ["expires ", ago(sb.expires_at)] : ""),
				h("span", { class: "grow" }),
				sb.status === "running" && h("button", { onclick: act(async () => { sb = await api("POST", base + "/pause"); header(); }, "Paused") }, "Pause"),
				sb.status === "paused" && h("button", { onclick: act(async () => { sb = await api("POST", base + "/resume"); header(); }, "Resumed") }, "Resume"),
				live && h("button", { onclick: act(async () => {
					const min = prompt("New TTL in minutes, from now:", "30");
					if (!min) return false;
					if (!(Number(min) > 0)) throw new Error("TTL must be a positive number of minutes");
					sb = await api("POST", base + "/timeout", { ttl_seconds: Math.max(1, Math.round(Number(min) * 60)) });
					header();
				}, "TTL updated") }, "Set TTL"),
				live && h("button", { class: "danger", onclick: act(async () => {
					if (!confirm("Delete " + sb.id + "?")) return false;
					await api("DELETE", base);
					location.hash = "#/sandboxes";
				}, "Deleted") }, "Delete"),
			).childNodes);
		}

		app.append(
			head,
			h("div", { class: "tabs" }, tabs.map((t) => h("button", { class: t === tab ? "on" : "", onclick: () => (location.hash = "#/sandboxes/" + encodeURIComponent(id) + "/" + t) }, t[0].toUpperCase() + t.slice(1)))),
			panel,
		);

		api("GET", base)
			.then((data) => {
				sb = data;
				header();
				if (tab === "terminal") terminal(panel, base, sb);
				else if (tab === "logs") logs(panel, base, sb);
				else overview(panel, sb);
			})
			.catch((e) => panel.append(h("div", { class: "empty" }, e.message)));
		every(10000, async () => {
			try {
				sb = await api("GET", base);
				header();
			} catch {}
		});
	}

	function overview(panel, sb) {
		const kv = (obj) => (obj && Object.keys(obj).length ? Object.entries(obj).map(([k, v]) => k + "=" + v).join("\n") : "–");
		const rows = [
			["Status", sb.status],
			["Backend", sb.backend],
			["Template", sb.template || "–"],
			["Image", sb.image],
			["CPU", sb.cpu],
			["Memory", sb.memory_mb + " MiB"],
			["Disk", sb.disk_mb + " MiB"],
			["PIDs", sb.pids],
			["Network", sb.network],
			["Egress allow", (sb.egress_allow || []).join(", ") || "–"],
			["Egress deny", (sb.egress_deny || []).join(", ") || "–"],
			["Idle timeout", sb.idle_timeout_seconds ? sb.idle_timeout_seconds + "s" : "–"],
			["Created", new Date(sb.created_at).toLocaleString()],
			["Expires", new Date(sb.expires_at).toLocaleString()],
			["Node", sb.node || "–"],
			["Labels", kv(sb.labels)],
			["Env", kv(sb.env)],
			["Secrets", (sb.secret_keys || []).join(", ") || "–"],
			["Mounts", (sb.mounts || []).map((m) => m.volume_id + " → " + m.path).join("\n") || "–"],
		];
		panel.append(
			h("div", { class: "card" }, h("dl", { class: "meta" }, rows.map(([k, v]) => [h("dt", {}, k), h("dd", { style: "white-space:pre-wrap" }, String(v))]))),
		);
	}

	// ---- terminal ----------------------------------------------------------

	const SHELL = ["sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash -l; else exec sh -l; fi"];

	function terminal(panel, base, sb) {
		if (sb.status !== "running") {
			panel.append(h("div", { class: "empty" }, "The terminal needs a running sandbox."));
			return;
		}
		if (!window.Terminal) {
			panel.append(h("div", { class: "empty" }, "The terminal library did not load."));
			return;
		}
		const box = h("div", { class: "term" });
		const status = h("span", { class: "muted small" }, "connecting…");
		const reconnect = h("button", { class: "sm", hidden: true, onclick: () => start() }, "New session");
		panel.append(h("div", { class: "bar" }, status, h("span", { class: "grow" }), reconnect), box);

		const term = new window.Terminal({
			cursorBlink: true,
			fontFamily: 'ui-monospace, "SFMono-Regular", Menlo, Consolas, monospace',
			fontSize: 13,
			theme: { background: "#0b0c0c", foreground: "#ececec", cursor: "#43b6a9" },
		});
		const fit = new window.FitAddon.FitAddon();
		term.loadAddon(fit);
		term.open(box);
		fit.fit();
		const enc = new TextEncoder();
		let ws = null;
		let termId = null;

		function stop() {
			if (ws) {
				ws.onclose = null;
				ws.close();
				ws = null;
			}
			if (termId) {
				fetch(base + "/terminals/" + encodeURIComponent(termId), {
					method: "DELETE",
					headers: { Authorization: "Bearer " + state.key },
					keepalive: true,
				}).catch(() => {});
				termId = null;
			}
		}

		async function start() {
			stop();
			term.reset();
			reconnect.hidden = true;
			status.textContent = "connecting…";
			try {
				const t = await api("POST", base + "/terminals", { cmd: SHELL, cols: term.cols, rows: term.rows });
				termId = t.id;
			} catch (e) {
				status.textContent = e.message;
				reconnect.hidden = false;
				return;
			}
			const proto = location.protocol === "https:" ? "wss:" : "ws:";
			// Browsers cannot set headers on a WebSocket, so the key goes in
			// the query. The server never logs query strings.
			ws = new WebSocket(proto + "//" + location.host + base + "/terminals/" + encodeURIComponent(termId) + "/ws?access_token=" + encodeURIComponent(state.key));
			ws.binaryType = "arraybuffer";
			ws.onopen = () => {
				status.textContent = "connected · " + termId;
				term.focus();
			};
			ws.onmessage = (ev) => {
				if (typeof ev.data !== "string") return void term.write(new Uint8Array(ev.data));
				try {
					const msg = JSON.parse(ev.data);
					if (msg.type === "exit") {
						term.write("\r\n\x1b[2m[exited with code " + msg.exit_code + "]\x1b[0m\r\n");
						termId = null;
					}
				} catch {}
			};
			ws.onclose = () => {
				status.textContent = "disconnected";
				reconnect.hidden = false;
				ws = null;
			};
		}

		const dataSub = term.onData((d) => ws && ws.readyState === 1 && ws.send(enc.encode(d)));
		const resizeSub = term.onResize(({ cols, rows }) => ws && ws.readyState === 1 && ws.send(JSON.stringify({ type: "resize", cols, rows })));
		const ro = new ResizeObserver(() => {
			try {
				fit.fit();
			} catch {}
		});
		ro.observe(box);
		const unload = () => stop();
		window.addEventListener("pagehide", unload);
		onCleanup(() => {
			window.removeEventListener("pagehide", unload);
			ro.disconnect();
			dataSub.dispose();
			resizeSub.dispose();
			stop();
			term.dispose();
		});
		start();
	}

	// ---- logs --------------------------------------------------------------

	// streamSSE reads server-sent events with fetch, so the key stays in a
	// header instead of the URL.
	async function streamSSE(path, signal, onEvent) {
		const res = await fetch(path, { headers: { Authorization: "Bearer " + state.key }, signal });
		if (!res.ok) {
			const data = await res.json().catch(() => null);
			throw new Error((data && data.error && data.error.message) || res.statusText);
		}
		const reader = res.body.getReader();
		const dec = new TextDecoder();
		let buf = "";
		for (;;) {
			const { value, done } = await reader.read();
			if (done) return;
			buf += dec.decode(value, { stream: true });
			let i;
			while ((i = buf.indexOf("\n\n")) >= 0) {
				const block = buf.slice(0, i);
				buf = buf.slice(i + 2);
				let event = "message";
				let data = "";
				for (const line of block.split("\n")) {
					if (line.startsWith("event: ")) event = line.slice(7);
					else if (line.startsWith("data: ")) data += line.slice(6);
				}
				try {
					onEvent(event, JSON.parse(data));
				} catch {}
			}
		}
	}

	function logs(panel, base, sb) {
		const list = h("div", { class: "procs" });
		const out = h("pre", { class: "log" }, h("span", { class: "sys" }, "Pick a process to follow its output, or run a command."));
		const title = h("span", { class: "muted small" });
		const cmd = h("input", { placeholder: "e.g. python -m http.server 8000", class: "grow", style: "min-width:0;font-family:var(--mono)" });
		let selected = null;
		let abort = null;

		function follow(proc) {
			if (abort) abort.abort();
			abort = new AbortController();
			selected = proc.id;
			title.textContent = proc.cmd.join(" ");
			out.replaceChildren();
			for (const b of list.children) b.classList.toggle("on", b.dataset.id === proc.id);
			const ctrl = abort;
			const append = (cls, text) => {
				const stick = out.scrollTop + out.clientHeight >= out.scrollHeight - 20;
				out.append(cls ? h("span", { class: cls }, text) : text);
				if (stick) out.scrollTop = out.scrollHeight;
			};
			streamSSE(base + "/processes/" + encodeURIComponent(proc.id) + "/stream", ctrl.signal, (event, data) => {
				if (event === "stdout") append("", data.data);
				else if (event === "stderr") append("stderr", data.data);
				else if (event === "exit") append("sys", "\n[exited with code " + data.exit_code + (data.truncated ? ", output truncated" : "") + "]\n");
				else if (event === "error") append("sys", "\n[" + data.message + "]\n");
			}).catch((e) => {
				if (!ctrl.signal.aborted) append("sys", "\n[" + e.message + "]\n");
			});
		}

		async function load() {
			let data;
			try {
				data = await api("GET", base + "/processes");
			} catch (e) {
				list.replaceChildren(h("div", { class: "empty small" }, e.message));
				return;
			}
			// Terminals are processes too; their output belongs to the terminal tab.
			const procs = data.processes.filter((p) => !p.tty).sort((a, b) => b.started_at.localeCompare(a.started_at));
			if (!procs.length) {
				list.replaceChildren(h("div", { class: "empty small" }, "No background processes yet."));
				return;
			}
			list.replaceChildren(
				...procs.map((p) =>
					h(
						"button",
						{ class: "proc" + (p.id === selected ? " on" : ""), "data-id": p.id, onclick: () => follow(p) },
						h("code", {}, p.cmd.join(" ")),
						h("span", { class: "small muted" }, p.status === "running" ? pill("running") : pill(p.exit_code === 0 ? "exited-0" : "exited", "exit " + p.exit_code), " pid " + p.pid + " · ", ago(p.started_at)),
					),
				),
			);
		}

		const run = h(
			"form",
			{ class: "bar", onsubmit: async (ev) => {
				ev.preventDefault();
				const text = cmd.value.trim();
				if (!text) return;
				try {
					const p = await api("POST", base + "/processes", { cmd: ["sh", "-c", text] });
					cmd.value = "";
					await load();
					follow(p);
				} catch (e) {
					toast(e.message, true);
				}
			} },
			cmd,
			h("button", { class: "primary", type: "submit", disabled: sb.status !== "running" }, "Run in background"),
		);

		panel.append(
			run,
			h(
				"div",
				{ class: "split" },
				h("div", {}, h("h2", {}, "Processes"), list),
				h("div", {}, h("div", { class: "bar", style: "margin-bottom:8px" }, h("h2", { style: "margin:0" }, "Output"), title), out),
			),
		);
		onCleanup(() => abort && abort.abort());
		if (sb.status !== "running") {
			list.replaceChildren(h("div", { class: "empty small" }, "Processes are listed while the sandbox runs."));
			return;
		}
		load();
		every(3000, load);
	}

	// ---- views: keys -------------------------------------------------------

	function keys() {
		const body = h("tbody");
		const secretBox = h("div");

		function keyStatus(k) {
			if (k.revoked_at) return "revoked";
			if (k.expires_at && new Date(k.expires_at) <= new Date()) return "expired";
			return "active";
		}

		async function load() {
			try {
				const data = await api("GET", "/v1/keys");
				const list = data.keys.sort((a, b) => b.created_at.localeCompare(a.created_at));
				body.replaceChildren(...list.map(row));
			} catch (e) {
				toast(e.message, true);
			}
		}

		function row(k) {
			const st = keyStatus(k);
			const mine = state.key.startsWith(k.prefix);
			return h(
				"tr",
				{},
				h("td", {}, k.name, mine && h("span", { class: "muted small" }, " (this browser)")),
				h("td", {}, h("code", {}, k.prefix + "…")),
				h("td", {}, k.scopes.join(", ")),
				h("td", {}, pill(st)),
				h("td", {}, ago(k.created_at)),
				h("td", {}, k.last_used_at ? ago(k.last_used_at) : "never"),
				h("td", {}, k.expires_at ? ago(k.expires_at) : "never"),
				h(
					"td",
					{ class: "actions" },
					st !== "revoked" && h("button", { class: "sm danger", onclick: act(async () => {
						if (!confirm("Revoke " + k.name + "? Clients using it stop working at once.")) return false;
						await api("DELETE", "/v1/keys/" + encodeURIComponent(k.id));
						if (mine) return signOut("You revoked the key this browser used.");
						await load();
					}, "Revoked") }, "Revoke"),
				),
			);
		}

		const name = h("input", { placeholder: "ci-runner", required: true, maxlength: 64 });
		const scope = h("select", {}, h("option", { value: "sandbox" }, "sandbox"), h("option", { value: "admin" }, "admin"));
		const expires = h("input", { type: "datetime-local" });
		const form = h(
			"form",
			{ class: "card form", onsubmit: async (ev) => {
				ev.preventDefault();
				const btn = form.querySelector("button[type=submit]");
				btn.disabled = true;
				const req = { name: name.value.trim(), scopes: [scope.value] };
				if (expires.value) req.expires_at = new Date(expires.value).toISOString();
				try {
					const k = await api("POST", "/v1/keys", req);
					name.value = "";
					expires.value = "";
					const code = h("code", {}, k.key);
					secretBox.replaceChildren(
						h(
							"div",
							{ class: "secret" },
							h("div", { style: "flex-basis:100%" }, h("strong", {}, k.name), h("span", { class: "muted small" }, " · shown once. Store it now; the server keeps only a hash.")),
							code,
							h("button", { class: "sm", type: "button", onclick: () => copy(k.key) }, "Copy"),
							h("button", { class: "sm ghost", type: "button", onclick: () => secretBox.replaceChildren() }, "Done"),
						),
					);
					await load();
				} catch (e) {
					toast(e.message, true);
				} finally {
					btn.disabled = false;
				}
			} },
			h("label", {}, "Name", name),
			h("label", {}, "Scope", scope),
			h("label", {}, "Expires (optional)", expires),
			h("button", { class: "primary", type: "submit" }, "Create key"),
		);

		app.append(
			h("div", { class: "bar" }, h("h1", {}, "API keys")),
			form,
			secretBox,
			h("div", { style: "height:16px" }),
			h(
				"table",
				{},
				h("thead", {}, h("tr", {}, ["Name", "Prefix", "Scopes", "Status", "Created", "Last used", "Expires", ""].map((t) => h("th", {}, t)))),
				body,
			),
		);
		load();
	}

	// ---- boot --------------------------------------------------------------

	document.getElementById("signout").addEventListener("click", () => signOut());
	window.addEventListener("hashchange", route);

	fetch("/", { headers: { Accept: "application/json" } })
		.then((r) => r.json())
		.then((d) => (document.getElementById("version").textContent = d.version || ""))
		.catch(() => {});

	route();
})();
