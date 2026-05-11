// Repair v2 — health checker dashboard.
//
// Settings live in the global Settings page; this controller only handles
// status, run/stop, and history. Polls /api/repair/status while a run is
// active so the UI reflects live progress.
class RepairManager {
    constructor() {
        this.api = (window.API || '/api').replace(/\/$/, '');
        this.statusTimer = null;
        this.activeRunId = null;
        this.brokenState = {items: [], page: 1, pageSize: 25};
        this.bind();
        this.loadAll();
    }

    bind() {
        const $ = (id) => document.getElementById(id);
        $('runNowBtn')?.addEventListener('click', () => this.runNow());
        $('stopRunBtn')?.addEventListener('click', () => this.stopRun());
        $('fixBrokenBtn')?.addEventListener('click', () => this.fixBroken());
        $('viewBrokenBtn')?.addEventListener('click', () => this.openBrokenModal());
        $('refreshHistoryBtn')?.addEventListener('click', () => this.loadHistory());
        $('refreshBrokenBtn')?.addEventListener('click', () => this.loadBroken());
        $('clearHistoryBtn')?.addEventListener('click', () => this.clearHistory());
        $('recheckMediaForm')?.addEventListener('submit', (e) => {
            e.preventDefault();
            this.recheckMedia();
        });
    }

    async loadAll() {
        await Promise.all([this.loadStatus(), this.loadHistory(), this.loadArrs()]);
    }

    openBrokenModal() {
        const modal = document.getElementById('brokenModal');
        if (!modal) return;
        // Fetch fresh data on every open.
        this.loadBroken();
        if (typeof modal.showModal === 'function') {
            modal.showModal();
        } else {
            modal.setAttribute('open', '');
        }
    }

    isBrokenModalOpen() {
        const modal = document.getElementById('brokenModal');
        return !!(modal && modal.open);
    }

    updateBrokenCount(n) {
        const badge = document.getElementById('brokenCountBadge');
        if (badge) {
            badge.textContent = n;
            badge.classList.toggle('hidden', n === 0);
        }
        const modalCount = document.getElementById('brokenModalCount');
        if (modalCount) modalCount.textContent = n;
    }

    async loadArrs() {
        try {
            const arrs = await this.fetchJSON(`${this.api}/arrs`);
            const sel = document.getElementById('recheckArr');
            if (!sel) return;
            const placeholder = sel.querySelector('option[value=""]');
            sel.innerHTML = '';
            if (placeholder) sel.appendChild(placeholder);
            for (const a of arrs || []) {
                if (!a || !a.name) continue;
                const opt = document.createElement('option');
                opt.value = a.name;
                opt.textContent = a.name;
                sel.appendChild(opt);
            }
        } catch (e) {
            console.error('Failed to load arrs', e);
        }
    }

    async recheckMedia() {
        const $ = (id) => document.getElementById(id);
        const mediaId = $('recheckMediaId').value.trim();
        if (!mediaId) {
            this.toast('Media id is required', 'warning');
            return;
        }
        const body = {
            arr: $('recheckArr').value,
            media_id: mediaId,
            fix: $('recheckFix').checked,
        };
        const btn = $('recheckMediaBtn');
        const out = $('recheckMediaResult');
        btn.disabled = true;
        out.classList.add('hidden');
        out.textContent = '';
        try {
            const res = await fetch(`${this.api}/repair/recheck/media`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(body),
            });
            const text = await res.text();
            let data = null;
            try {
                data = text ? JSON.parse(text) : null;
            } catch { /* leave null */
            }
            if (!res.ok) {
                const msg = (data && (data.error || data.message)) || text || `HTTP ${res.status}`;
                throw new Error(msg);
            }
            // Server kicks the recheck off in the background and returns a
            // run record immediately. Reload so the dashboard reflects the
            // new active run; status polling takes it from there.
            this.toast('Recheck started', 'success');
            window.location.reload();
        } catch (e) {
            out.classList.remove('hidden');
            out.innerHTML = `<span class="text-error">Recheck failed: ${this.escape(e.message)}</span>`;
            btn.disabled = false;
        }
    }

    renderRecheckResult(container, run) {
        if (!container) return;
        if (!run) {
            container.classList.add('hidden');
            return;
        }
        container.classList.remove('hidden');
        const stats = run.stats || {};
        const status = run.status || 'unknown';
        const cls = {
            running: 'badge-info',
            completed: 'badge-success',
            failed: 'badge-error',
            cancelled: 'badge-warning',
        }[status] || 'badge-ghost';
        container.innerHTML = `
            <div class="flex flex-wrap gap-3 items-center">
                <span class="badge ${cls}">${this.escape(status)}</span>
                <span class="font-mono text-xs">${this.escape(run.id || '')}</span>
                ${run.source ? `<span class="opacity-70 text-xs">${this.escape(run.source)}</span>` : ''}
            </div>
            <div class="grid grid-cols-2 sm:grid-cols-4 lg:grid-cols-6 gap-2 mt-3 text-xs">
                <div>Candidates: <strong>${stats.candidates ?? 0}</strong></div>
                <div>Probed: <strong>${stats.probed ?? 0}</strong></div>
                <div class="${stats.broken ? 'text-error' : ''}">Broken: <strong>${stats.broken ?? 0}</strong></div>
                <div class="${stats.healthy ? 'text-success' : ''}">Healthy: <strong>${stats.healthy ?? 0}</strong></div>
                <div class="${stats.repaired ? 'text-success' : ''}">Repaired: <strong>${stats.repaired ?? 0}</strong></div>
                <div class="${stats.repair_failed ? 'text-error' : ''}">Repair fail: <strong>${stats.repair_failed ?? 0}</strong></div>
            </div>
            ${run.error ? `<div class="mt-2 text-error text-xs">${this.escape(run.error)}</div>` : ''}
        `;
    }

    escape(s) {
        const div = document.createElement('div');
        div.textContent = s == null ? '' : String(s);
        return div.innerHTML;
    }

    async runNow() {
        try {
            const res = await fetch(`${this.api}/repair/run`, {method: 'POST'});
            if (!res.ok) {
                const txt = await res.text();
                throw new Error(txt || `HTTP ${res.status}`);
            }
            this.toast('Sweep started', 'success');
            await this.loadStatus();
        } catch (e) {
            this.toast(`Run failed: ${e.message}`, 'error');
        }
    }

    async fixBroken() {
        if (!confirm('Send delete + re-search for every currently broken entry to its Arr?')) return;
        const btn = document.getElementById('fixBrokenBtn');
        if (btn) btn.disabled = true;
        try {
            const res = await fetch(`${this.api}/repair/fix`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({}),
            });
            const text = await res.text();
            if (!res.ok) throw new Error(text || `HTTP ${res.status}`);
            this.toast('Fix-broken started', 'success');
            window.location.reload();
        } catch (e) {
            this.toast(`Fix failed: ${e.message}`, 'error');
            if (btn) btn.disabled = false;
        }
    }

    async stopRun() {
        try {
            const res = await fetch(`${this.api}/repair/stop`, {method: 'POST'});
            if (!res.ok) {
                const txt = await res.text();
                throw new Error(txt || `HTTP ${res.status}`);
            }
            this.toast('Stop requested', 'info');
            await this.loadStatus();
        } catch (e) {
            this.toast(`Stop failed: ${e.message}`, 'error');
        }
    }

    async clearHistory() {
        if (!confirm('Clear all run history?')) return;
        try {
            const res = await fetch(`${this.api}/repair/runs`, {method: 'DELETE'});
            if (!res.ok) throw new Error(`HTTP ${res.status}`);
            await this.loadHistory();
        } catch (e) {
            this.toast(`Clear failed: ${e.message}`, 'error');
        }
    }

    async loadStatus() {
        try {
            const status = await this.fetchJSON(`${this.api}/repair/status`);
            this.renderStatus(status || {});
            this.scheduleStatusPoll(status);
        } catch (e) {
            console.error('Failed to load status', e);
        }
    }

    scheduleStatusPoll(status) {
        const isRunning = !!(status && status.active_run);
        const wasRunning = this.wasRunning === true;
        this.wasRunning = isRunning;
        // Run ended → refresh history. Only refetch the broken modal contents
        // when it's actually open; the count badge is already updated from
        // status.health_counts on every poll.
        if (wasRunning && !isRunning) {
            this.loadHistory();
            if (this.isBrokenModalOpen()) this.loadBroken();
        }
        if (this.statusTimer) {
            clearTimeout(this.statusTimer);
            this.statusTimer = null;
        }
        const delay = isRunning ? 2000 : 15000;
        this.statusTimer = setTimeout(() => this.loadStatus(), delay);
    }

    renderStatus(status) {
        const line = document.getElementById('repairStatusLine');
        const stop = document.getElementById('stopRunBtn');
        const run = document.getElementById('runNowBtn');
        const panel = document.getElementById('activeRunPanel');
        const grid = document.getElementById('healthCountsGrid');

        if (!status.enabled) {
            line.textContent = 'Repair is disabled. Enable it in Settings → Repair, or click "Run now" for a one-off check.';
        } else {
            const next = status.next_run_at ? new Date(status.next_run_at).toLocaleString() : 'unknown';
            line.textContent = `Repair enabled · next scheduled run: ${next}`;
        }

        const brokenCount = (status.health_counts || {}).broken || 0;
        this.updateBrokenCount(brokenCount);
        const fix = document.getElementById('fixBrokenBtn');
        if (fix) fix.disabled = !!status.active_run || brokenCount === 0;
        const view = document.getElementById('viewBrokenBtn');
        if (view) view.disabled = brokenCount === 0;

        if (status.active_run) {
            stop.disabled = false;
            run.disabled = true;
            panel.classList.remove('hidden');
            this.activeRunId = status.active_run.id;
            document.getElementById('activeRunStage').textContent = status.active_run.stage || 'running';
            document.getElementById('activeRunIdText').textContent = status.active_run.id || '-';
            document.getElementById('activeRunStarted').textContent = status.active_run.started_at
                ? new Date(status.active_run.started_at).toLocaleString()
                : '-';
            this.renderRunStats(document.getElementById('activeRunStats'), status.active_run.stats || {});
        } else {
            stop.disabled = true;
            run.disabled = false;
            panel.classList.add('hidden');
            this.activeRunId = null;
            document.getElementById('activeRunStage').textContent = '-';
            document.getElementById('activeRunIdText').textContent = '-';
            document.getElementById('activeRunStarted').textContent = '-';
        }

        const counts = status.health_counts || {};
        const order = ['healthy', 'broken', 'repairing', 'stale', 'unknown', 'unsupported'];
        grid.innerHTML = '';
        for (const key of order) {
            const n = counts[key] || 0;
            const card = document.createElement('div');
            card.className = 'stat bg-base-200 rounded-box p-3';
            card.innerHTML = `
                <div class="stat-title text-xs capitalize">${key}</div>
                <div class="stat-value text-lg ${this.healthColor(key)}">${n}</div>
            `;
            grid.appendChild(card);
        }
    }

    renderRunStats(container, stats) {
        if (!container) return;
        const fields = [
            ['candidates', 'Candidates'],
            ['skipped_fresh', 'Skipped'],
            ['probed', 'Probed'],
            ['healthy', 'Healthy'],
            ['broken', 'Broken'],
            ['repaired', 'Repaired'],
            ['repair_failed', 'Repair fail'],
        ];
        container.innerHTML = '';
        for (const [k, label] of fields) {
            const el = document.createElement('div');
            el.className = 'bg-base-100 rounded p-2';
            el.innerHTML = `<div class="text-[10px] opacity-60 uppercase">${label}</div><div class="font-mono">${stats[k] || 0}</div>`;
            container.appendChild(el);
        }
    }

    healthColor(status) {
        switch (status) {
            case 'healthy':
                return 'text-success';
            case 'broken':
                return 'text-error';
            case 'repairing':
                return 'text-info';
            case 'stale':
                return 'text-warning';
            case 'unsupported':
                return 'text-base-content/60';
            default:
                return '';
        }
    }

    async loadHistory() {
        try {
            const runs = await this.fetchJSON(`${this.api}/repair/runs`);
            this.renderHistory(runs || []);
        } catch (e) {
            console.error('Failed to load history', e);
        }
    }

    async loadBroken() {
        try {
            const list = await this.fetchJSON(`${this.api}/repair/health?status=broken`);
            this.renderBroken(list || []);
        } catch (e) {
            console.error('Failed to load broken entries', e);
        }
    }

    renderBroken(entries) {
        this.updateBrokenCount(entries.length);

        // Sort: most recently failed first, then by name.
        entries.sort((a, b) => {
            const ta = a.last_failed_at ? new Date(a.last_failed_at).getTime() : 0;
            const tb = b.last_failed_at ? new Date(b.last_failed_at).getTime() : 0;
            if (ta !== tb) return tb - ta;
            return (a.entry_name || '').localeCompare(b.entry_name || '');
        });

        this.brokenState.items = entries;
        // Clamp current page so a shrinking list doesn't strand the user on an empty page.
        const totalPages = Math.max(1, Math.ceil(entries.length / this.brokenState.pageSize));
        if (this.brokenState.page > totalPages) this.brokenState.page = totalPages;
        if (this.brokenState.page < 1) this.brokenState.page = 1;
        this.renderBrokenPage();
    }

    renderBrokenPage() {
        const tbody = document.getElementById('brokenTableBody');
        const empty = document.getElementById('noBrokenMessage');
        if (!tbody) return;
        tbody.innerHTML = '';

        const {items, page, pageSize} = this.brokenState;
        if (!items.length) {
            empty?.classList.remove('hidden');
            this.renderBrokenPagination();
            return;
        }
        empty?.classList.add('hidden');

        const start = (page - 1) * pageSize;
        const slice = items.slice(start, start + pageSize);

        for (const h of slice) {
            const rowId = `broken-row-${this.slug(h.entry_name)}`;
            const fileCount = h.file_count ?? 0;
            const brokenCount = h.broken_count ?? (h.broken_files?.length ?? 0);
            const lastChecked = h.last_checked_at ? new Date(h.last_checked_at).toLocaleString() : '-';
            const lastRepair = h.last_repair_at ? new Date(h.last_repair_at).toLocaleString() : '-';
            const reason = h.failure_reason || '-';

            const tr = document.createElement('tr');
            tr.className = 'cursor-pointer hover:bg-base-200';
            tr.innerHTML = `
                <td class="w-8">
                    <i class="bi bi-chevron-right transition-transform" id="${rowId}-caret"></i>
                </td>
                <td class="font-mono text-sm break-all">${this.escape(h.entry_name)}</td>
                <td><span class="badge badge-ghost badge-sm">${this.escape(h.protocol || 'unknown')}</span></td>
                <td>${fileCount}</td>
                <td class="text-error font-medium">${brokenCount}</td>
                <td class="text-xs">${this.escape(reason)}</td>
                <td class="text-xs">${lastChecked}</td>
                <td class="text-xs">${lastRepair}</td>
                <td class="text-right whitespace-nowrap">
                    <button class="btn btn-xs btn-outline" data-action="recheck" data-name="${this.escapeAttr(h.entry_name)}" aria-label="Recheck ${this.escape(h.entry_name)}">
                        <i class="bi bi-search-heart"></i>
                    </button>
                    <button class="btn btn-xs btn-error btn-outline" data-action="fix" data-name="${this.escapeAttr(h.entry_name)}" aria-label="Fix ${this.escape(h.entry_name)}">
                        <i class="bi bi-bandaid"></i>
                    </button>
                </td>
            `;
            tbody.appendChild(tr);

            const detail = document.createElement('tr');
            detail.id = rowId;
            detail.className = 'hidden';
            detail.innerHTML = `
                <td colspan="9" class="bg-base-200/40 p-0">
                    <div class="p-4 space-y-2">
                        ${this.renderBrokenFiles(h.broken_files || [])}
                    </div>
                </td>
            `;
            tbody.appendChild(detail);

            tr.addEventListener('click', (ev) => {
                if (ev.target.closest('[data-action]')) return;
                const hidden = detail.classList.toggle('hidden');
                const caret = document.getElementById(`${rowId}-caret`);
                if (caret) caret.style.transform = hidden ? '' : 'rotate(90deg)';
            });
            tr.querySelector('[data-action="recheck"]')?.addEventListener('click', (ev) => {
                ev.stopPropagation();
                this.recheckOne(h.entry_name);
            });
            tr.querySelector('[data-action="fix"]')?.addEventListener('click', (ev) => {
                ev.stopPropagation();
                this.fixOne(h.entry_name);
            });
        }
        this.renderBrokenPagination();
    }

    renderBrokenPagination() {
        const bar = document.getElementById('brokenPaginationBar');
        const info = document.getElementById('brokenPaginationInfo');
        const controls = document.getElementById('brokenPaginationControls');
        if (!bar || !info || !controls) return;

        const {items, page, pageSize} = this.brokenState;
        const total = items.length;
        if (total === 0) {
            bar.classList.add('hidden');
            return;
        }
        bar.classList.remove('hidden');

        const totalPages = Math.max(1, Math.ceil(total / pageSize));
        const start = (page - 1) * pageSize + 1;
        const end = Math.min(start + pageSize - 1, total);
        info.textContent = `Showing ${start}-${end} of ${total}`;

        if (totalPages <= 1) {
            controls.innerHTML = '';
            return;
        }

        let html = `<button class="join-item btn btn-sm ${page === 1 ? 'btn-disabled' : ''}"
                            onclick="window.repairManager.goToBrokenPage(${page - 1})">«</button>`;
        for (let i = 1; i <= totalPages; i++) {
            if (i === 1 || i === totalPages || (i >= page - 2 && i <= page + 2)) {
                html += `<button class="join-item btn btn-sm ${i === page ? 'btn-active' : ''}"
                                onclick="window.repairManager.goToBrokenPage(${i})">${i}</button>`;
            } else if (i === page - 3 || i === page + 3) {
                html += `<button class="join-item btn btn-sm btn-disabled">…</button>`;
            }
        }
        html += `<button class="join-item btn btn-sm ${page === totalPages ? 'btn-disabled' : ''}"
                         onclick="window.repairManager.goToBrokenPage(${page + 1})">»</button>`;
        controls.innerHTML = html;
    }

    goToBrokenPage(p) {
        const totalPages = Math.max(1, Math.ceil(this.brokenState.items.length / this.brokenState.pageSize));
        if (p < 1 || p > totalPages || p === this.brokenState.page) return;
        this.brokenState.page = p;
        this.renderBrokenPage();
    }

    renderBrokenFiles(files) {
        if (!files.length) {
            return `<div class="text-sm opacity-60">No broken file details.</div>`;
        }
        const rows = files.map(f => {
            const arr = f.arr_name ? `${this.escape(f.arr_name)}${f.arr_kind ? ` (${this.escape(f.arr_kind)})` : ''}` : '<span class="opacity-50">—</span>';
            const ids = [];
            if (f.media_id) ids.push(`media:${f.media_id}`);
            if (f.episode_id) ids.push(`ep:${f.episode_id}`);
            if (f.arr_file_id) ids.push(`file:${f.arr_file_id}`);
            const idStr = ids.length ? `<span class="font-mono text-[10px] opacity-70">${ids.join(' · ')}</span>` : '';
            const size = f.size ? this.formatBytes(f.size) : '-';
            return `
                <tr>
                    <td class="font-mono text-xs break-all">${this.escape(f.file_name || '')}</td>
                    <td class="text-xs">${this.escape(f.reason || '-')}</td>
                    <td class="text-xs">${size}</td>
                    <td class="text-xs">${arr}</td>
                    <td>${idStr}</td>
                </tr>
            `;
        }).join('');
        return `
            <div class="overflow-x-auto">
                <table class="table table-xs">
                    <thead><tr><th>File</th><th>Reason</th><th>Size</th><th>Arr</th><th>Ids</th></tr></thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>
        `;
    }

    async recheckOne(name) {
        try {
            const res = await fetch(`${this.api}/repair/health/${encodeURIComponent(name)}/check`, {method: 'POST'});
            if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`);
            this.toast(`Recheck started for ${name}`, 'success');
            // Recheck flips the entry to repairing; refresh shortly so the row updates.
            setTimeout(() => { this.loadBroken(); this.loadStatus(); }, 800);
        } catch (e) {
            this.toast(`Recheck failed: ${e.message}`, 'error');
        }
    }

    async fixOne(name) {
        if (!confirm(`Send delete + re-search for "${name}" to its Arr?`)) return;
        try {
            const res = await fetch(`${this.api}/repair/fix`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({names: [name]}),
            });
            if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`);
            this.toast(`Fix started for ${name}`, 'success');
            window.location.reload();
        } catch (e) {
            this.toast(`Fix failed: ${e.message}`, 'error');
        }
    }

    slug(s) {
        return String(s || '').replace(/[^a-zA-Z0-9_-]+/g, '_');
    }

    escapeAttr(s) {
        return String(s == null ? '' : s).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    }

    formatBytes(n) {
        if (!n || n < 0) return '-';
        const units = ['B', 'KB', 'MB', 'GB', 'TB'];
        let i = 0;
        let v = n;
        while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
        return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`;
    }

    renderHistory(runs) {
        const tbody = document.getElementById('runsTableBody');
        const empty = document.getElementById('noRunsMessage');
        tbody.innerHTML = '';
        if (!runs.length) {
            empty.classList.remove('hidden');
            return;
        }
        empty.classList.add('hidden');
        for (const run of runs) {
            const tr = document.createElement('tr');
            const start = run.started_at ? new Date(run.started_at) : null;
            const end = run.completed_at ? new Date(run.completed_at) : null;
            const duration = start && end ? this.formatDuration(end - start) : (start ? 'running' : '-');
            tr.innerHTML = `
                <td class="font-mono text-sm">${start ? start.toLocaleString() : '-'}</td>
                <td>${run.trigger || '-'}</td>
                <td>${this.statusBadge(run.status)}</td>
                <td>${run.stats?.probed ?? 0}</td>
                <td class="${run.stats?.broken ? 'text-error font-medium' : ''}">${run.stats?.broken ?? 0}</td>
                <td class="${run.stats?.repaired ? 'text-success font-medium' : ''}">${run.stats?.repaired ?? 0}</td>
                <td>${duration}</td>
                <td class="text-xs text-error">${run.error || ''}</td>
            `;
            tbody.appendChild(tr);
        }
    }

    statusBadge(status) {
        const cls = {
            running: 'badge-info',
            completed: 'badge-success',
            failed: 'badge-error',
            cancelled: 'badge-warning',
        }[status] || 'badge-ghost';
        return `<span class="badge ${cls}">${status || 'unknown'}</span>`;
    }

    formatDuration(ms) {
        if (!ms || ms < 0) return '-';
        const s = Math.round(ms / 1000);
        if (s < 60) return `${s}s`;
        const m = Math.floor(s / 60);
        const r = s % 60;
        return `${m}m ${r}s`;
    }

    async fetchJSON(url) {
        const res = await fetch(url, {credentials: 'same-origin'});
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
    }

    toast(message, type = 'info') {
        if (typeof window.toast === 'function') return window.toast(message, type);
        if (typeof window.showToast === 'function') return window.showToast(message, type);
        console.log(`[${type}]`, message);
    }
}

window.RepairManager = RepairManager;
