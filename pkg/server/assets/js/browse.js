// WebDAV-style File Browser with URL-based navigation
class FileBrowser {
    constructor() {
        this.state = {
            currentPath: '/',
            currentPage: 1,
            itemsPerPage: 20,
            searchQuery: '',
            sortBy: 'name',
            sortOrder: 'asc',
            entries: [],
            total: 0,
            totalPages: 0,
            parentDir: null,
            selectedEntry: null,
            selectedEntries: new Set(),
            selectedEntryData: new Map(),
            health: new Map()
        };

        this.refs = {
            breadcrumbNav: document.getElementById('breadcrumbNav'),
            refreshBtn: document.getElementById('refreshBtn'),
            searchInput: document.getElementById('searchInput'),
            pageSizeSelect: document.getElementById('pageSizeSelect'),
            sortHeaderButtons: document.querySelectorAll('[data-sort-key]'),
            fileBrowserList: document.getElementById('fileBrowserList'),
            paginationInfo: document.getElementById('paginationInfo'),
            paginationControls: document.getElementById('paginationControls'),
            emptyState: document.getElementById('emptyState'),

            // Bulk actions
            selectAllCheckbox: document.getElementById('selectAllCheckbox'),
            bulkActionsBar: document.getElementById('bulkActionsBar'),
            selectedCount: document.getElementById('selectedCount'),
            bulkDownloadBtn: document.getElementById('bulkDownloadBtn'),
            bulkDeleteBtn: document.getElementById('bulkDeleteBtn'),
            clearSelectionBtn: document.getElementById('clearSelectionBtn'),

            // Context menu
            contextMenu: document.getElementById('contextMenu'),
            contextDownload: document.getElementById('contextDownload'),
            contextDelete: document.getElementById('contextDelete'),

            // Stale NZB cleanup
            staleNZBsBtn: document.getElementById('staleNZBsBtn'),
            staleNZBsModal: document.getElementById('staleNZBsModal'),
            staleNZBsSummary: document.getElementById('staleNZBsSummary'),
            staleNZBsResult: document.getElementById('staleNZBsResult'),
            staleNZBsMajorityWarning: document.getElementById('staleNZBsMajorityWarning'),
            staleNZBsProgress: document.getElementById('staleNZBsProgress'),
            staleNZBsProgressCount: document.getElementById('staleNZBsProgressCount'),
            staleNZBsProgressBar: document.getElementById('staleNZBsProgressBar'),
            staleNZBsSelectAll: document.getElementById('staleNZBsSelectAll'),
            staleNZBsSelectionSummary: document.getElementById('staleNZBsSelectionSummary'),
            staleNZBsTableBody: document.getElementById('staleNZBsTableBody'),
            staleNZBsEmpty: document.getElementById('staleNZBsEmpty'),
            staleNZBsFilterInput: document.getElementById('staleNZBsFilterInput'),
            staleNZBsCancelBtn: document.getElementById('staleNZBsCancelBtn'),
            staleNZBsDeleteAllBtn: document.getElementById('staleNZBsDeleteAllBtn'),
            staleNZBsDeleteSelectedBtn: document.getElementById('staleNZBsDeleteSelectedBtn')
        };

        // Stale NZB cleanup state, kept separate from the main browse state:
        // preview holds the last preview response ({entries, orphans, totals}).
        // selected tracks which rows are currently checked in the modal, as
        // "entry:<id>" / "orphan:<id>" keys - entries and orphans share one
        // selection/confirm/delete flow but live in different ID namespaces,
        // so the prefix keeps them distinguishable without risking a collision.
        // filterQuery narrows which rows renderStaleNZBsTable shows (name
        // match, both categories) without touching selection - it's a view
        // filter, not a selection filter.
        // progressTimer is the setInterval id for polling
        // /api/browse/stale-nzbs/progress while a preview or cleanup request
        // is outstanding - see start/stopStaleNZBsProgressPolling.
        this.staleNZBs = {
            preview: null,
            selected: new Set(),
            filterQuery: '',
            progressTimer: null
        };

        this.searchTimeout = null;
        this.activeLoadController = null;
        this.loadRequestSeq = 0;

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadStateFromURL();
        this.loadEntries();
    }

    bindEvents() {
        // Refresh
        this.refs.refreshBtn.addEventListener('click', () => this.refresh());

        // Search with debounce
        this.refs.searchInput.addEventListener('input', (e) => {
            clearTimeout(this.searchTimeout);
            this.searchTimeout = setTimeout(() => {
                this.state.searchQuery = e.target.value;
                this.state.currentPage = 1;
                this.updateURL();
                this.refresh();
            }, 300);
        });

        // Page size
        this.refs.pageSizeSelect.addEventListener('change', (e) => {
            this.state.itemsPerPage = parseInt(e.target.value);
            this.state.currentPage = 1;
            this.updateURL();
            this.refresh();
        });

        // Sort headers
        if (this.refs.sortHeaderButtons) {
            this.refs.sortHeaderButtons.forEach(btn => {
                btn.addEventListener('click', () => {
                    this.handleSortHeaderClick(btn.dataset.sortKey);
                });
            });
        }

        // Select all checkbox
        if (this.refs.selectAllCheckbox) {
            this.refs.selectAllCheckbox.addEventListener('change', (e) => {
                this.handleSelectAll(e.target.checked);
            });
        }

        // Bulk action buttons
        if (this.refs.bulkDownloadBtn) {
            this.refs.bulkDownloadBtn.addEventListener('click', () => this.bulkDownload());
        }
        if (this.refs.bulkDeleteBtn) {
            this.refs.bulkDeleteBtn.addEventListener('click', () => this.bulkDelete());
        }
        if (this.refs.clearSelectionBtn) {
            this.refs.clearSelectionBtn.addEventListener('click', () => this.clearSelection());
        }

        // Stale NZB cleanup
        if (this.refs.staleNZBsBtn) {
            this.refs.staleNZBsBtn.addEventListener('click', () => this.openStaleNZBsModal());
        }
        if (this.refs.staleNZBsSelectAll) {
            this.refs.staleNZBsSelectAll.addEventListener('change', (e) => this.toggleStaleNZBsSelectAll(e.target.checked));
        }
        if (this.refs.staleNZBsFilterInput) {
            this.refs.staleNZBsFilterInput.addEventListener('input', (e) => {
                this.staleNZBs.filterQuery = e.target.value;
                this.renderStaleNZBsTable();
            });
        }
        if (this.refs.staleNZBsCancelBtn) {
            this.refs.staleNZBsCancelBtn.addEventListener('click', () => this.refs.staleNZBsModal?.close?.());
        }
        if (this.refs.staleNZBsDeleteSelectedBtn) {
            this.bindStaleNZBsConfirmButton(this.refs.staleNZBsDeleteSelectedBtn, () => Array.from(this.staleNZBs.selected));
        }
        if (this.refs.staleNZBsDeleteAllBtn) {
            this.bindStaleNZBsConfirmButton(this.refs.staleNZBsDeleteAllBtn, () => this.getStaleNZBRows().map(r => r.key));
        }

        // Hide context menu on click outside
        document.addEventListener('click', (e) => {
            if (!this.refs.contextMenu.contains(e.target)) {
                this.hideContextMenu();
            }
        });

        // Prevent default context menu
        document.addEventListener('contextmenu', (e) => {
            const row = e.target.closest('tr[data-entry]');
            if (row) {
                e.preventDefault();
            }
        });

        // Handle browser back/forward
        window.addEventListener('popstate', () => {
            this.loadStateFromURL();
            this.refresh();
        });
    }

    loadStateFromURL() {
        const params = new URLSearchParams(window.location.search);

        // Load path from URL
        this.state.currentPath = params.get('path') || '/';

        // Load page from URL
        const page = parseInt(params.get('page'));
        this.state.currentPage = page > 0 ? page : 1;

        // Load search from URL
        this.state.searchQuery = params.get('search') || '';
        if (this.refs.searchInput) {
            this.refs.searchInput.value = this.state.searchQuery;
        }

        // Load page size from URL
        const limit = parseInt(params.get('limit'));
        this.state.itemsPerPage = limit > 0 ? limit : 20;
        if (this.refs.pageSizeSelect) {
            this.refs.pageSizeSelect.value = this.state.itemsPerPage.toString();
        }

        // Load sorting from URL
        const sortBy = params.get('sort_by');
        this.state.sortBy = this.isValidSortBy(sortBy) ? sortBy : 'name';

        const sortOrder = params.get('sort_order');
        this.state.sortOrder = sortOrder === 'desc' ? 'desc' : 'asc';
        this.updateSortHeaderIndicators();
    }

    updateURL() {
        const params = new URLSearchParams();

        // Always include path
        if (this.state.currentPath !== '/') {
            params.set('path', this.state.currentPath);
        }

        // Include page if not 1
        if (this.state.currentPage > 1) {
            params.set('page', this.state.currentPage);
        }

        // Include search if present
        if (this.state.searchQuery) {
            params.set('search', this.state.searchQuery);
        }

        // Include limit if not default
        if (this.state.itemsPerPage !== 20) {
            params.set('limit', this.state.itemsPerPage);
        }

        // Include sort params if not defaults
        if (this.state.sortBy !== 'name') {
            params.set('sort_by', this.state.sortBy);
        }
        if (this.state.sortOrder !== 'asc') {
            params.set('sort_order', this.state.sortOrder);
        }

        const newURL = `${window.location.pathname}${params.toString() ? '?' + params.toString() : ''}`;
        window.history.pushState({}, '', newURL);
    }

    navigate(path) {
        this.state.currentPath = path;
        this.state.currentPage = 1;
        this.updateURL();
        this.loadEntries();
    }

    refresh() {
        this.loadEntries();
    }

    async loadEntries() {
        const requestId = ++this.loadRequestSeq;
        if (this.activeLoadController) {
            this.activeLoadController.abort();
        }
        this.activeLoadController = new AbortController();

        try {
            // Build API URL based on path depth
            const pathParts = this.state.currentPath.split('/').filter(p => p);
            let apiUrl = `${window.urlBase}api/browse`;

            if (pathParts.length === 0) {
                apiUrl += '/';
            } else if (pathParts.length === 1) {
                apiUrl += `/${encodeURIComponent(pathParts[0])}`;
            } else if (pathParts.length === 2) {
                apiUrl += `/${encodeURIComponent(pathParts[0])}/${encodeURIComponent(pathParts[1])}`;
            } else if (pathParts.length === 3) {
                apiUrl += `/${encodeURIComponent(pathParts[0])}/${encodeURIComponent(pathParts[1])}/${encodeURIComponent(pathParts[2])}`;
            }

            // Add query params
            const params = new URLSearchParams({
                page: this.state.currentPage,
                limit: this.state.itemsPerPage,
                sort_by: this.state.sortBy,
                sort_order: this.state.sortOrder
            });

            if (this.state.searchQuery) {
                params.set('search', this.state.searchQuery);
            }

            const response = await fetch(`${apiUrl}?${params}`, {signal: this.activeLoadController.signal});
            if (!response.ok) throw new Error('Failed to load directory');

            const data = await response.json();
            if (requestId !== this.loadRequestSeq) {
                return;
            }
            this.state.entries = data.entries || [];
            this.state.total = data.total || 0;
            this.state.totalPages = data.total_pages || 0;
            this.state.parentDir = data.parent_dir;

            this.updateBreadcrumbs();
            this.renderEntries();
            this.renderPagination();
            this.loadHealthForEntries();
        } catch (error) {
            if (error.name === 'AbortError') {
                return;
            }
            console.error('Error loading entries:', error);
            window.createToast('Failed to load directory', 'error');
        }
    }

    async loadHealthForEntries() {
        const names = Array.from(new Set(this.state.entries
            .filter((e) => e && e.name)
            .map((e) => e.name)));
        if (names.length === 0) return;
        const results = await Promise.all(names.map(async (name) => {
            try {
                const res = await fetch(`${window.urlBase}api/repair/health/${encodeURIComponent(name)}`);
                if (!res.ok) return [name, null];
                const state = await res.json();
                return [name, state];
            } catch {
                return [name, null];
            }
        }));
        for (const [name, state] of results) {
            if (state) this.state.health.set(name, state);
        }
        this.refreshHealthBadges();
    }

    refreshHealthBadges() {
        document.querySelectorAll('[data-health-cell]').forEach((cell) => {
            const name = cell.getAttribute('data-health-cell');
            cell.innerHTML = this.healthBadge(this.state.health.get(name));
        });
    }

    healthBadge(state) {
        if (!state) {
            return '<span class="badge badge-ghost badge-sm">unknown</span>';
        }
        const colors = {
            healthy: 'badge-success',
            broken: 'badge-error',
            repairing: 'badge-info',
            stale: 'badge-warning',
            unsupported: 'badge-ghost',
            unknown: 'badge-ghost',
        };
        const cls = colors[state.status] || 'badge-ghost';
        const tooltip = state.last_checked_at
            ? `last checked ${new Date(state.last_checked_at).toLocaleString()}`
            : 'never checked';
        return `<span class="badge ${cls} badge-sm" title="${this.escapeAttr(tooltip)}">${this.escapeHtml(state.status || 'unknown')}</span>`;
    }

    async recheckEntry(name) {
        try {
            window.createToast?.(`Rechecking ${name}…`, 'info');
            const url = `${window.urlBase}api/repair/health/${encodeURIComponent(name)}/check`;
            const res = await fetch(url, {method: 'POST'});
            if (!res.ok) {
                const txt = await res.text();
                throw new Error(txt || `HTTP ${res.status}`);
            }
            const state = await res.json();
            this.state.health.set(name, state);
            this.refreshHealthBadges();
            window.createToast?.(`Health: ${state.status}`, state.status === 'broken' ? 'warning' : 'success');
        } catch (e) {
            console.error('Recheck failed', e);
            window.createToast?.(`Recheck failed: ${e.message}`, 'error');
        }
    }

    updateBreadcrumbs() {
        const parts = this.state.currentPath.split('/').filter(p => p);

        let html = `<li><a href="${window.urlBase}browse" data-path="/">
            <i class="bi bi-house-door"></i> Home
        </a></li>`;

        let currentPath = '';
        parts.forEach(part => {
            currentPath += '/' + part;
            const displayName = decodeURIComponent(part);
            html += `<li><a href="${window.urlBase}browse?path=${encodeURIComponent(currentPath)}" data-path="${currentPath}">${this.escapeHtml(displayName)}</a></li>`;
        });

        this.refs.breadcrumbNav.innerHTML = html;

        // Add click handlers to override default link behavior
        this.refs.breadcrumbNav.querySelectorAll('a').forEach(link => {
            link.addEventListener('click', (e) => {
                e.preventDefault();
                const path = e.currentTarget.dataset.path;
                this.navigate(path);
            });
        });

        // "Clean up stale NZBs" only makes sense at the top-level nzbs group,
        // not while browsing into a specific release's files.
        if (this.refs.staleNZBsBtn) {
            this.refs.staleNZBsBtn.classList.toggle('hidden', this.state.currentPath !== '/nzbs');
        }
    }

    renderEntries() {
        if (this.state.entries.length === 0) {
            this.refs.fileBrowserList.innerHTML = '';
            this.refs.emptyState.classList.remove('hidden');
            this.refs.paginationInfo.textContent = 'No items found';
            return;
        }

        this.refs.emptyState.classList.add('hidden');

        this.refs.fileBrowserList.innerHTML = this.state.entries.map(entry => {
            const icon = entry.is_dir ?
                '<i class="bi bi-folder-fill text-warning text-lg transition-colors group-hover:text-warning-content"></i>' :
                '<i class="bi bi-file-earmark text-info transition-colors group-hover:text-info-content"></i>';

            const entryId = entry.info_hash || entry.path;
            const isChecked = this.state.selectedEntries.has(entryId);

            return `
                <tr class="group hover:bg-base-200 transition-colors"
                    data-entry='${JSON.stringify(entry)}'
                    data-entry-id="${this.escapeAttr(entryId)}"
                    oncontextmenu="window.fileBrowser.showContextMenu(event, ${this.escapeAttr(JSON.stringify(entry))});">
                    <td onclick="event.stopPropagation();">
                        <label class="cursor-pointer">
                            <input type="checkbox"
                                   class="checkbox checkbox-sm checkbox-primary entry-checkbox"
                                   data-entry-id="${this.escapeAttr(entryId)}"
                                   ${isChecked ? 'checked' : ''}
                                   onchange="window.fileBrowser.handleEntrySelect('${this.escapeAttr(entryId)}', this.checked, ${this.escapeAttr(JSON.stringify(entry))})">
                        </label>
                    </td>
                    <td>${icon}</td>
                    <td onclick="window.fileBrowser.handleEntryClick('${this.escapeJs(entry.path)}', ${entry.is_dir}, '${this.escapeJs(entry.name)}');" class="cursor-pointer hover:text-primary transition-colors">
                        <span class="font-medium">${this.escapeHtml(entry.name)}</span>
                    </td>
                    <td>
                        ${entry.size <= 0 ? '-' : this.formatSize(entry.size)}
                    </td>
                    <td class="text-xs text-base-content/70">
                        ${entry.mod_time || '-'}
                    </td>
                    <td>
                        ${entry.active_debrid ? `<span>${this.escapeHtml(entry.active_debrid)}</span>` : '-'}
                    </td>
                    <td data-health-cell="${this.escapeAttr(entry.name)}">
                        ${this.healthBadge(this.state.health.get(entry.name))}
                    </td>
                    <td onclick="event.stopPropagation();">
                        <div class="dropdown dropdown-end">
                            <label tabindex="0" class="btn btn-ghost btn-xs">
                                <i class="bi bi-three-dots-vertical"></i>
                            </label>
                            <ul tabindex="0" class="dropdown-content menu p-2 shadow bg-base-200 rounded-box w-52 z-50">
                                ${!entry.is_dir ? `
                                    <li><a onclick="window.fileBrowser.downloadFile('${this.escapeJs(entry.path)}', '${this.escapeJs(entry.name)}')">
                                        <i class="bi bi-download"></i> Download
                                    </a></li>
                                ` : ''}
                                <li><a onclick="window.fileBrowser.recheckEntry('${this.escapeJs(entry.name)}')">
                                    <i class="bi bi-search-heart"></i> Recheck health
                                </a></li>
                                ${entry.can_delete ? `
                                    <li><a onclick="window.fileBrowser.deleteTorrent('${this.escapeJs(entry.info_hash)}', '${this.escapeJs(entry.name)}')" class="text-error">
                                        <i class="bi bi-trash"></i> Delete
                                    </a></li>
                                ` : ''}
                            </ul>
                        </div>
                    </td>
                </tr>
            `;
        }).join('');

        this.updateSelectionUI();
    }

    renderPagination() {
        const start = (this.state.currentPage - 1) * this.state.itemsPerPage + 1;
        const end = Math.min(start + this.state.itemsPerPage - 1, this.state.total);

        this.refs.paginationInfo.textContent = this.state.total > 0
            ? `Showing ${start}-${end} of ${this.state.total} items`
            : 'No items';

        if (this.state.totalPages <= 1) {
            this.refs.paginationControls.innerHTML = '';
            return;
        }

        let html = `
            <button class="join-item btn btn-sm ${this.state.currentPage === 1 ? 'btn-disabled' : ''}"
                    onclick="window.fileBrowser.goToPage(${this.state.currentPage - 1})"
                    ${this.state.currentPage === 1 ? 'disabled' : ''}>
                <i class="bi bi-chevron-left"></i>
            </button>
        `;

        // Smart pagination: show first, last, current, and nearby pages
        for (let i = 1; i <= this.state.totalPages; i++) {
            if (i === 1 || i === this.state.totalPages ||
                (i >= this.state.currentPage - 2 && i <= this.state.currentPage + 2)) {
                html += `
                    <button class="join-item btn btn-sm ${i === this.state.currentPage ? 'btn-active' : ''}"
                            onclick="window.fileBrowser.goToPage(${i})">${i}</button>
                `;
            } else if (i === this.state.currentPage - 3 || i === this.state.currentPage + 3) {
                html += `<button class="join-item btn btn-sm btn-disabled" disabled>...</button>`;
            }
        }

        html += `
            <button class="join-item btn btn-sm ${this.state.currentPage === this.state.totalPages ? 'btn-disabled' : ''}"
                    onclick="window.fileBrowser.goToPage(${this.state.currentPage + 1})"
                    ${this.state.currentPage === this.state.totalPages ? 'disabled' : ''}>
                <i class="bi bi-chevron-right"></i>
            </button>
        `;

        this.refs.paginationControls.innerHTML = html;
    }

    goToPage(page) {
        if (page < 1 || page > this.state.totalPages) return;
        this.state.currentPage = page;
        this.updateURL();
        this.refresh();
    }

    isValidSortBy(sortBy) {
        return ['name', 'size', 'mod_time', 'active_debrid'].includes(sortBy);
    }

    handleSortHeaderClick(sortBy) {
        if (!this.isValidSortBy(sortBy)) {
            return;
        }

        if (this.state.sortBy === sortBy) {
            this.state.sortOrder = this.state.sortOrder === 'asc' ? 'desc' : 'asc';
        } else {
            this.state.sortBy = sortBy;
            // Fresh column defaults: text asc, numeric/time desc.
            this.state.sortOrder = (sortBy === 'size' || sortBy === 'mod_time') ? 'desc' : 'asc';
        }

        this.state.currentPage = 1;
        this.updateSortHeaderIndicators();
        this.updateURL();
        this.refresh();
    }

    updateSortHeaderIndicators() {
        const sortKeys = ['name', 'size', 'mod_time', 'active_debrid'];
        sortKeys.forEach(key => {
            const indicator = document.getElementById(`sortIndicator-${key}`);
            if (!indicator) return;

            if (this.state.sortBy === key) {
                indicator.className = this.state.sortOrder === 'asc' ? 'bi bi-sort-up text-xs' : 'bi bi-sort-down text-xs';
            } else {
                indicator.className = 'bi bi-arrow-down-up text-xs';
            }
        });
    }

    handleEntryClick(path, isDir, name) {
        if (isDir) {
            this.navigate(path);
        } else {
            this.downloadFile(path, name);
        }
    }

    showContextMenu(event, entry) {
        event.preventDefault();
        event.stopPropagation();

        // Position context menu
        this.refs.contextMenu.style.left = event.pageX + 'px';
        this.refs.contextMenu.style.top = event.pageY + 'px';
        this.refs.contextMenu.classList.remove('hidden');

        // Show/hide appropriate menu items
        if (!entry.is_dir) {
            this.refs.contextDownload.classList.remove('hidden');
            this.refs.contextDownload.onclick = () => this.downloadFile(entry.path, entry.name);
        } else {
            this.refs.contextDownload.classList.add('hidden');
        }

        if (entry.can_delete) {
            this.refs.contextDelete.classList.remove('hidden');
            this.refs.contextDelete.onclick = () => this.deleteTorrent(entry.info_hash, entry.name);
        } else {
            this.refs.contextDelete.classList.add('hidden');
        }

        this.state.selectedEntry = entry;
    }

    hideContextMenu() {
        this.refs.contextMenu.classList.add('hidden');
    }

    downloadFile(path, fileName) {
        this.hideContextMenu();

        // Extract torrent and file names from path
        const pathParts = path.split('/').filter(p => p);
        if (pathParts.length < 3) return;

        const torrentName = pathParts[pathParts.length - 2];
        const file = pathParts[pathParts.length - 1];

        const downloadUrl = `${window.urlBase}api/browse/download/${encodeURIComponent(torrentName)}/${encodeURIComponent(file)}`;
        window.open(downloadUrl, '_blank');
    }

    async deleteTorrent(infoHash, name) {
        this.hideContextMenu();

        if (!confirm(`Delete "${name}"?\n\nThis will remove the item from the system.`)) {
            return;
        }

        try {
            const response = await fetch(`${window.urlBase}api/browse/torrents/${infoHash}`, {
                method: 'DELETE'
            });

            if (!response.ok) throw new Error('Failed to delete entry');

            window.createToast('Item deleted successfully', 'success');
            this.refresh();
        } catch (error) {
            console.error('Error deleting item:', error);
            window.createToast('Failed to delete item', 'error');
        }
    }

    // Utility methods
    formatSize(bytes) {
        if (!bytes || bytes === 0) return '0 B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    escapeAttr(text) {
        if (typeof text !== 'string') {
            text = JSON.stringify(text);
        }
        return text.replace(/'/g, '&#39;').replace(/"/g, '&quot;');
    }

    escapeJs(text) {
        if (typeof text !== 'string') {
            text = String(text);
        }
        return text.replace(/\\/g, '\\\\').replace(/'/g, "\\'").replace(/"/g, '\\"').replace(/\n/g, '\\n').replace(/\r/g, '\\r');
    }

    // Multi-select methods
    handleEntrySelect(entryId, checked, entry) {
        if (checked) {
            this.state.selectedEntries.add(entryId);
            if (entry) {
                this.state.selectedEntryData.set(entryId, entry);
            }
        } else {
            this.state.selectedEntries.delete(entryId);
            this.state.selectedEntryData.delete(entryId);
        }
        this.updateSelectionUI();
    }

    handleSelectAll(checked) {
        if (checked) {
            this.state.entries.forEach(entry => {
                const entryId = entry.info_hash || entry.path;
                this.state.selectedEntries.add(entryId);
                this.state.selectedEntryData.set(entryId, entry);
            });
        } else {
            this.state.entries.forEach(entry => {
                const entryId = entry.info_hash || entry.path;
                this.state.selectedEntries.delete(entryId);
                this.state.selectedEntryData.delete(entryId);
            });
        }

        // Update all checkboxes
        document.querySelectorAll('.entry-checkbox').forEach(checkbox => {
            checkbox.checked = checked;
        });

        this.updateSelectionUI();
    }

    updateSelectionUI() {
        const selectedCount = this.state.selectedEntries.size;

        // Update count
        if (this.refs.selectedCount) {
            this.refs.selectedCount.textContent = selectedCount;
        }

        // Show/hide bulk actions bar
        if (this.refs.bulkActionsBar) {
            if (selectedCount > 0) {
                this.refs.bulkActionsBar.classList.remove('hidden');
            } else {
                this.refs.bulkActionsBar.classList.add('hidden');
            }
        }

        // Update select all checkbox state
        if (this.refs.selectAllCheckbox) {
            const allSelected = this.state.entries.length > 0 &&
                this.state.entries.every(entry => {
                    const entryId = entry.info_hash || entry.path;
                    return this.state.selectedEntries.has(entryId);
                });
            this.refs.selectAllCheckbox.checked = allSelected;
            this.refs.selectAllCheckbox.indeterminate = selectedCount > 0 && !allSelected;
        }
    }

    clearSelection() {
        this.state.selectedEntries.clear();
        this.state.selectedEntryData.clear();
        document.querySelectorAll('.entry-checkbox').forEach(checkbox => {
            checkbox.checked = false;
        });
        this.updateSelectionUI();
    }

    bulkDownload() {
        const selectedEntries = this.getSelectedEntries();
        const files = selectedEntries.filter(e => !e.is_dir);

        if (files.length === 0) {
            window.createToast('No files selected for download', 'warning');
            return;
        }

        files.forEach(entry => {
            this.downloadFile(entry.path, entry.name);
        });

        window.createToast(`Downloading ${files.length} file(s)`, 'success');
    }

    async bulkDelete() {
        const selectedEntries = this.getSelectedEntries();
        const torrents = selectedEntries.filter(e => e.can_delete && e.info_hash);

        if (torrents.length === 0) {
            window.createToast('No items selected for deletion', 'warning');
            return;
        }

        const names = torrents.map(t => t.name).join('\n');
        if (!confirm(`Delete ${torrents.length} torrent(s)?\n\n${names}\n\nThis will remove the items from the management system.`)) {
            return;
        }

        const ids = [...new Set(torrents.map(t => t.info_hash).filter(Boolean))];
        try {
            const response = await fetch(`${window.urlBase}api/browse/torrents/batch`, {
                method: 'DELETE',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({ids})
            });
            if (!response.ok) throw new Error('Failed to delete selected items');
            window.createToast(`Deleted ${ids.length} torrent(s)`, 'success');
            this.clearSelection();
            this.refresh();
        } catch (error) {
            console.error('Error deleting selected items:', error);
            window.createToast('Failed to delete selected items', 'error');
        }
    }

    getSelectedEntries() {
        return Array.from(this.state.selectedEntryData.values()).filter(Boolean);
    }

    // === Stale NZB cleanup ===

    async openStaleNZBsModal() {
        const modal = this.refs.staleNZBsModal;
        if (!modal) return;

        this.refs.staleNZBsResult?.classList.add('hidden');
        this.refs.staleNZBsMajorityWarning?.classList.add('hidden');
        this.refs.staleNZBsProgress?.classList.add('hidden');
        this.refs.staleNZBsProgress?.classList.remove('flex');
        this.refs.staleNZBsSummary.textContent = 'Checking every NZB entry against Sonarr/Radarr…';
        this.refs.staleNZBsTableBody.innerHTML = '';
        this.refs.staleNZBsEmpty?.classList.add('hidden');
        this.staleNZBs.filterQuery = '';
        if (this.refs.staleNZBsFilterInput) this.refs.staleNZBsFilterInput.value = '';
        this.setStaleNZBsButtonsBusy(true);

        if (typeof modal.showModal === 'function') {
            modal.showModal();
        } else {
            modal.setAttribute('open', '');
        }

        this.startStaleNZBsProgressPolling();
        try {
            const response = await fetch(`${window.urlBase}api/browse/stale-nzbs/preview`);
            const text = await response.text();
            if (!response.ok) throw new Error(text || `HTTP ${response.status}`);
            const preview = text ? JSON.parse(text) : {entries: [], orphans: [], totals: {}};
            this.staleNZBs.preview = preview;
            this.staleNZBs.selected = new Set([
                ...(preview.entries || []).map(e => `entry:${e.id}`),
                ...(preview.orphans || []).map(o => `orphan:${o.id}`)
            ]);
            this.renderStaleNZBsTable();
        } catch (error) {
            console.error('Failed to load stale NZB preview:', error);
            this.refs.staleNZBsSummary.textContent = 'Failed to check for stale NZBs.';
            this.showStaleNZBsError(`Failed to check for stale NZBs: ${error.message}`);
        } finally {
            this.stopStaleNZBsProgressPolling();
            this.setStaleNZBsButtonsBusy(false);
        }
    }

    // startStaleNZBsProgressPolling polls the progress endpoint every 500ms
    // while a preview/cleanup request is outstanding. Progress is purely
    // decorative - the main request's own response is always the source of
    // truth for what actually happened, so a poll failure is swallowed and
    // never surfaced to the user.
    startStaleNZBsProgressPolling() {
        this.stopStaleNZBsProgressPolling();
        const poll = async () => {
            try {
                const response = await fetch(`${window.urlBase}api/browse/stale-nzbs/progress`);
                if (!response.ok) return;
                const progress = await response.json();
                this.renderStaleNZBsProgress(progress);
            } catch {
                // Decorative only - see method comment.
            }
        };
        poll();
        this.staleNZBs.progressTimer = setInterval(poll, 500);
    }

    stopStaleNZBsProgressPolling() {
        if (this.staleNZBs.progressTimer) {
            clearInterval(this.staleNZBs.progressTimer);
            this.staleNZBs.progressTimer = null;
        }
        this.refs.staleNZBsProgress?.classList.add('hidden');
        this.refs.staleNZBsProgress?.classList.remove('flex');
    }

    // renderStaleNZBsProgress swaps the loading label to the current phase
    // and shows a counter (plus a bar, when the phase's total is known) next
    // to it. total <= 0 means "unknown" - shown as a running count instead
    // of a bar, since there's nothing to size a bar against yet.
    renderStaleNZBsProgress(progress) {
        if (!progress || !progress.running) {
            this.refs.staleNZBsProgress?.classList.add('hidden');
            this.refs.staleNZBsProgress?.classList.remove('flex');
            return;
        }
        if (progress.phase) {
            this.refs.staleNZBsSummary.textContent = progress.phase;
        }
        if (!this.refs.staleNZBsProgress) return;
        this.refs.staleNZBsProgress.classList.remove('hidden');
        this.refs.staleNZBsProgress.classList.add('flex');

        const done = progress.done || 0;
        if (progress.total > 0) {
            const pct = Math.min(100, Math.round((done / progress.total) * 100));
            if (this.refs.staleNZBsProgressBar) {
                this.refs.staleNZBsProgressBar.classList.remove('hidden');
                this.refs.staleNZBsProgressBar.value = pct;
            }
            if (this.refs.staleNZBsProgressCount) {
                this.refs.staleNZBsProgressCount.textContent = `${done.toLocaleString()} / ${progress.total.toLocaleString()}`;
            }
        } else {
            this.refs.staleNZBsProgressBar?.classList.add('hidden');
            if (this.refs.staleNZBsProgressCount) {
                this.refs.staleNZBsProgressCount.textContent = `${done.toLocaleString()} scanned…`;
            }
        }
    }

    // showStaleNZBsError displays a failure inside the modal body itself
    // (the staleNZBsResult alert, styled as an error) rather than only a
    // toast - a toast disappears on its own before it can always be read;
    // this stays until the next successful preview/cleanup replaces it or
    // the user closes the modal.
    showStaleNZBsError(message) {
        window.createToast(message, 'error');
        if (!this.refs.staleNZBsResult) return;
        this.refs.staleNZBsResult.textContent = message;
        this.refs.staleNZBsResult.classList.remove('hidden', 'alert-success', 'alert-warning');
        this.refs.staleNZBsResult.classList.add('alert-error');
    }

    setStaleNZBsButtonsBusy(busy) {
        [this.refs.staleNZBsDeleteAllBtn, this.refs.staleNZBsDeleteSelectedBtn].forEach(btn => {
            if (btn) btn.disabled = busy;
        });
    }

    // getStaleNZBRows returns preview.entries and preview.orphans as one
    // combined list, each tagged with a "kind" and the prefixed selection
    // key it shares with this.staleNZBs.selected - the single source both
    // categories render, select and delete through.
    getStaleNZBRows() {
        const entries = (this.staleNZBs.preview?.entries || []).map(e => ({...e, kind: 'entry', key: `entry:${e.id}`}));
        const orphans = (this.staleNZBs.preview?.orphans || []).map(o => ({...o, kind: 'orphan', key: `orphan:${o.id}`}));
        return [...entries, ...orphans];
    }

    // getFilteredStaleNZBRows applies filterQuery (name match, case
    // insensitive) on top of getStaleNZBRows - a view filter only, it never
    // changes what's selected, just what's currently shown/selectable via
    // "Select all".
    getFilteredStaleNZBRows() {
        const rows = this.getStaleNZBRows();
        const q = (this.staleNZBs.filterQuery || '').trim().toLowerCase();
        if (!q) return rows;
        return rows.filter(r => (r.name || '').toLowerCase().includes(q));
    }

    renderStaleNZBsTable() {
        const rows = this.getStaleNZBRows();
        const filteredRows = this.getFilteredStaleNZBRows();
        const totals = this.staleNZBs.preview?.totals || {};

        this.refs.staleNZBsMajorityWarning?.classList.toggle('hidden', !totals.majorityStale);

        const totalGB = this.formatSize(totals.totalBytes || 0);
        const cacheGB = this.formatSize(totals.cacheBytes || 0);
        const nzbMetaGB = this.formatSize(totals.nzbMetaBytes || 0);
        this.refs.staleNZBsSummary.textContent = rows.length
            ? `${totals.count || rows.length} stale items — ${totalGB} of local disk reclaimable (${cacheGB} cache, ${nzbMetaGB} nzb/meta).`
            : 'No stale NZB entries found.';

        this.refs.staleNZBsTableBody.innerHTML = '';
        this.refs.staleNZBsEmpty?.classList.toggle('hidden', filteredRows.length > 0);
        if (this.refs.staleNZBsEmpty) {
            this.refs.staleNZBsEmpty.querySelector('.stale-nzb-empty-text')?.remove();
            const text = rows.length && !filteredRows.length
                ? `No matches for "${this.staleNZBs.filterQuery.trim()}".`
                : 'No stale NZB entries found.';
            const span = document.createElement('span');
            span.className = 'stale-nzb-empty-text';
            span.textContent = text;
            this.refs.staleNZBsEmpty.appendChild(span);
        }
        this.refs.staleNZBsSelectAll.checked = filteredRows.length > 0 && filteredRows.every(r => this.staleNZBs.selected.has(r.key));
        this.refs.staleNZBsSelectAll.disabled = filteredRows.length === 0;

        for (const row of filteredRows) {
            const tr = document.createElement('tr');
            const added = row.addedOn ? new Date(row.addedOn).toLocaleDateString() : '-';
            const badge = row.kind === 'orphan'
                ? '<span class="badge badge-ghost badge-xs mr-1" title="No database entry - disk file only">orphan</span>'
                : '';
            const checkedAttr = this.staleNZBs.selected.has(row.key) ? 'checked' : '';
            tr.innerHTML = `
                <td><input type="checkbox" class="checkbox checkbox-sm checkbox-primary stale-nzb-checkbox" data-key="${this.escapeAttr(row.key)}" ${checkedAttr}></td>
                <td class="font-mono text-xs break-all">${badge}${this.escapeHtml(row.name)}</td>
                <td class="text-xs whitespace-nowrap">${added}</td>
                <td class="text-xs text-right whitespace-nowrap">${this.formatSize(row.localBytes?.total || 0)}</td>
            `;
            this.refs.staleNZBsTableBody.appendChild(tr);
        }

        this.refs.staleNZBsTableBody.querySelectorAll('.stale-nzb-checkbox').forEach(cb => {
            cb.addEventListener('change', (e) => {
                const key = e.target.dataset.key;
                if (e.target.checked) {
                    this.staleNZBs.selected.add(key);
                } else {
                    this.staleNZBs.selected.delete(key);
                }
                this.updateStaleNZBsSelectionSummary();
            });
        });

        this.updateStaleNZBsSelectionSummary();
    }

    // toggleStaleNZBsSelectAll only affects rows the current filter shows -
    // a row hidden by filterQuery keeps whatever selection state it already
    // had, matching the checkboxes actually visible in the modal.
    toggleStaleNZBsSelectAll(checked) {
        const rows = this.getFilteredStaleNZBRows();
        for (const row of rows) {
            if (checked) {
                this.staleNZBs.selected.add(row.key);
            } else {
                this.staleNZBs.selected.delete(row.key);
            }
        }
        this.refs.staleNZBsTableBody.querySelectorAll('.stale-nzb-checkbox').forEach(cb => {
            cb.checked = checked;
        });
        this.updateStaleNZBsSelectionSummary();
    }

    updateStaleNZBsSelectionSummary() {
        const rows = this.getStaleNZBRows();
        const selected = rows.filter(r => this.staleNZBs.selected.has(r.key));
        const bytes = selected.reduce((sum, r) => sum + (r.localBytes?.total || 0), 0);

        if (this.refs.staleNZBsSelectionSummary) {
            this.refs.staleNZBsSelectionSummary.textContent = selected.length
                ? `${selected.length} selected — ${this.formatSize(bytes)}`
                : 'Nothing selected';
        }
        if (this.refs.staleNZBsDeleteSelectedBtn) {
            this.refs.staleNZBsDeleteSelectedBtn.textContent = `Delete selected (${selected.length})`;
            this.refs.staleNZBsDeleteSelectedBtn.dataset.confirming = '';
        }
        if (this.refs.staleNZBsDeleteAllBtn) {
            this.refs.staleNZBsDeleteAllBtn.dataset.confirming = '';
            this.refs.staleNZBsDeleteAllBtn.textContent = 'Delete all';
        }
    }

    // bindStaleNZBsConfirmButton wires a cheap two-step confirm onto btn: the
    // first click swaps the label to "Really delete N entries?"; a second
    // click within the window actually runs the cleanup. keysFn is called
    // fresh on each click so "Delete all" always reflects the current
    // preview regardless of the individual-row selection. keys are the
    // prefixed "entry:<id>" / "orphan:<id>" strings from getStaleNZBRows.
    bindStaleNZBsConfirmButton(btn, keysFn) {
        btn.addEventListener('click', () => {
            const keys = keysFn();
            if (!keys.length) {
                window.createToast('No entries to delete', 'warning');
                return;
            }
            if (btn.dataset.confirming === 'true') {
                this.runStaleNZBsCleanup(keys);
                return;
            }
            btn.dataset.confirming = 'true';
            btn.dataset.originalLabel = btn.textContent;
            btn.textContent = `Really delete ${keys.length} entries?`;
            setTimeout(() => {
                if (btn.dataset.confirming === 'true') {
                    btn.dataset.confirming = '';
                    if (btn.dataset.originalLabel) btn.textContent = btn.dataset.originalLabel;
                }
            }, 4000);
        });
    }

    async runStaleNZBsCleanup(keys) {
        this.setStaleNZBsButtonsBusy(true);
        this.startStaleNZBsProgressPolling();
        try {
            const entryIds = keys.filter(k => k.startsWith('entry:')).map(k => k.slice('entry:'.length));
            const orphanIds = keys.filter(k => k.startsWith('orphan:')).map(k => k.slice('orphan:'.length));
            const response = await fetch(`${window.urlBase}api/browse/stale-nzbs/cleanup`, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({entryIds, orphanIds})
            });
            const text = await response.text();
            let result = null;
            try {
                result = text ? JSON.parse(text) : null;
            } catch { /* leave null */
            }
            if (!response.ok) throw new Error((result && result.error) || text || `HTTP ${response.status}`);

            const freedTotal = this.formatSize(result?.freed?.totalBytes || 0);
            const freedCache = this.formatSize(result?.freed?.cacheBytes || 0);
            const freedNzbMeta = this.formatSize(result?.freed?.nzbMetaBytes || 0);
            const skippedCount = (result?.skipped || []).length;
            window.createToast(
                `Freed ${freedTotal} (${freedCache} cache, ${freedNzbMeta} nzb/meta) — deleted ${result?.deleted || 0} entries, skipped ${skippedCount}`,
                'success'
            );

            if (this.refs.staleNZBsResult) {
                const lines = [`Deleted ${result?.deleted || 0}, failed ${result?.failed || 0}.`];
                if (skippedCount) {
                    lines.push('Skipped:');
                    for (const s of result.skipped) {
                        lines.push(`- ${s.name || s.id}: ${s.reason}`);
                    }
                }
                this.refs.staleNZBsResult.textContent = lines.join('\n');
                this.refs.staleNZBsResult.classList.remove('hidden', 'alert-error');
                this.refs.staleNZBsResult.classList.toggle('alert-warning', skippedCount > 0 || (result?.failed || 0) > 0);
                this.refs.staleNZBsResult.classList.toggle('alert-success', skippedCount === 0 && (result?.failed || 0) === 0);
                this.refs.staleNZBsResult.style.whiteSpace = 'pre-line';
            }

            // Re-preview so the table reflects what's actually left, and
            // refresh the underlying nzbs listing behind the modal.
            await this.openStaleNZBsModal();
            this.refresh();
        } catch (error) {
            console.error('Stale NZB cleanup failed:', error);
            this.showStaleNZBsError(`Cleanup failed: ${error.message}`);
        } finally {
            this.stopStaleNZBsProgressPolling();
            this.setStaleNZBsButtonsBusy(false);
        }
    }

    async bulkRecheck() {
        const selected = this.getSelectedEntries();
        if (!selected.length) {
            window.createToast('No items selected', 'warning');
            return;
        }
        for (const entry of selected) {
            if (entry?.name) await this.recheckEntry(entry.name);
        }
    }
}
