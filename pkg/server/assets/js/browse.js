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
            currentKind: '',
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
            emptyStateMessage: document.getElementById('emptyStateMessage'),
            emptyStateVirtualFolderLink: document.getElementById('emptyStateVirtualFolderLink'),
            currentViewNotice: document.getElementById('currentViewNotice'),

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
            contextDelete: document.getElementById('contextDelete')
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
        this.clearSelection();
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
            this.state.currentKind = data.current_kind || '';

            this.updateBreadcrumbs();
            this.updateCurrentViewNotice();
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
            .filter((e) => e && e.name && e.kind === 'entry')
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

    updateCurrentViewNotice() {
        const isVirtual = this.state.currentKind === 'virtual';
        this.refs.currentViewNotice?.classList.toggle('hidden', !isVirtual);
        if (this.refs.emptyStateMessage) {
            this.refs.emptyStateMessage.textContent = isVirtual
                ? 'No current items match this virtual folder’s conditions.'
                : 'No files or folders found here.';
        }
        this.refs.emptyStateVirtualFolderLink?.classList.toggle('hidden', !isVirtual);
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
            const icons = {
                virtual: '<i class="bi bi-folder-symlink text-info text-lg" aria-hidden="true"></i>',
                provider: '<i class="bi bi-cloud text-secondary text-lg" aria-hidden="true"></i>',
                system: '<i class="bi bi-folder-fill text-warning text-lg" aria-hidden="true"></i>',
                entry: '<i class="bi bi-folder-fill text-warning text-lg" aria-hidden="true"></i>',
                file: '<i class="bi bi-file-earmark text-info" aria-hidden="true"></i>'
            };
            const icon = entry.is_dir ? (icons[entry.kind] || icons.entry) : icons.file;
            const badges = {
                virtual: '<span class="badge badge-info badge-outline badge-sm">Virtual view</span>',
                provider: '<span class="badge badge-secondary badge-outline badge-sm">Provider</span>',
                system: '<span class="badge badge-ghost badge-sm">Library</span>'
            };
            const kindBadge = badges[entry.kind] || '';
            const isActionable = this.isActionableEntry(entry);
            const canOpen = entry.is_dir || isActionable;

            const entryId = entry.info_hash || entry.path;
            const isChecked = this.state.selectedEntries.has(entryId);
            const actionMenu = isActionable ? `
                <div class="dropdown dropdown-end">
                    <button type="button" tabindex="0" class="btn btn-ghost btn-xs" aria-label="Actions for ${this.escapeAttr(entry.name)}">
                        <i class="bi bi-three-dots-vertical" aria-hidden="true"></i>
                    </button>
                    <ul tabindex="0" class="dropdown-content menu p-2 shadow bg-base-200 rounded-box w-56 z-50" aria-label="Actions">
                        ${!entry.is_dir ? `<li><button type="button" onclick="window.fileBrowser.downloadFile('${this.escapeJs(entry.path)}', '${this.escapeJs(entry.name)}')"><i class="bi bi-download" aria-hidden="true"></i> Download</button></li>` : ''}
                        ${entry.kind === 'entry' ? `<li><button type="button" onclick="window.fileBrowser.recheckEntry('${this.escapeJs(entry.name)}')"><i class="bi bi-search-heart" aria-hidden="true"></i> Recheck health</button></li>` : ''}
                        ${entry.can_delete ? `<li><button type="button" onclick="window.fileBrowser.deleteTorrent('${this.escapeJs(entry.info_hash)}', '${this.escapeJs(entry.name)}')" class="text-error"><i class="bi bi-trash" aria-hidden="true"></i> Delete original item</button></li>` : ''}
                    </ul>
                </div>` : '<span aria-hidden="true">—</span>';

            return `
                <tr class="group hover:bg-base-200 transition-colors"
                    data-entry='${this.escapeAttr(JSON.stringify(entry))}'
                    data-entry-id="${this.escapeAttr(entryId)}"
                    ${isActionable ? `oncontextmenu="window.fileBrowser.showContextMenu(event, ${this.escapeAttr(JSON.stringify(entry))});"` : ''}>
                    <td onclick="event.stopPropagation();">
                        <label class="${isActionable ? 'cursor-pointer' : ''}">
                            <span class="sr-only">Select ${this.escapeHtml(entry.name)}</span>
                            <input type="checkbox" aria-label="Select ${this.escapeAttr(entry.name)}"
                                   class="checkbox checkbox-sm checkbox-primary entry-checkbox"
                                   data-entry-id="${this.escapeAttr(entryId)}"
                                   ${isChecked ? 'checked' : ''}
                                   ${isActionable ? '' : 'disabled'}
                                   onchange="window.fileBrowser.handleEntrySelect('${this.escapeAttr(entryId)}', this.checked, ${this.escapeAttr(JSON.stringify(entry))})">
                        </label>
                    </td>
                    <td>${icon}</td>
                    <td>
                        ${canOpen ? `<button type="button" onclick="window.fileBrowser.handleEntryClick('${this.escapeJs(entry.path)}', ${entry.is_dir}, '${this.escapeJs(entry.name)}');" class="text-left hover:text-primary transition-colors"><span class="font-medium">${this.escapeHtml(entry.name)}</span></button>` : `<span class="font-medium">${this.escapeHtml(entry.name)}</span>`}
                        ${kindBadge ? `<span class="ml-2">${kindBadge}</span>` : ''}
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
                    <td ${entry.kind === 'entry' ? `data-health-cell="${this.escapeAttr(entry.name)}"` : ''}>
                        ${entry.kind === 'entry' ? this.healthBadge(this.state.health.get(entry.name)) : '—'}
                    </td>
                    <td onclick="event.stopPropagation();">
                        ${actionMenu}
                    </td>
                </tr>
            `;
        }).join('');

        this.updateSelectionUI();
    }

    isActionableEntry(entry) {
        if (!entry || ['system', 'provider', 'virtual'].includes(entry.kind)) return false;
        return Boolean(entry.kind === 'entry' || entry.kind === 'file' || entry.can_delete || !entry.is_dir);
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

        if (!confirm(`Permanently delete the original item "${name}"?\n\nThis removes it from every library and virtual folder, and removes its placement from the provider. This cannot be undone from Decypharr.`)) {
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
        return text
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/'/g, '&#39;')
            .replace(/"/g, '&quot;');
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
            this.state.entries.filter(entry => this.isActionableEntry(entry)).forEach(entry => {
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
            const actionableEntries = this.state.entries.filter(entry => this.isActionableEntry(entry));
            const allSelected = actionableEntries.length > 0 &&
                actionableEntries.every(entry => {
                    const entryId = entry.info_hash || entry.path;
                    return this.state.selectedEntries.has(entryId);
                });
            this.refs.selectAllCheckbox.checked = allSelected;
            this.refs.selectAllCheckbox.indeterminate = selectedCount > 0 && !allSelected;
            this.refs.selectAllCheckbox.disabled = actionableEntries.length === 0;
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
        if (!confirm(`Permanently delete ${torrents.length} original item(s)?\n\n${names}\n\nThis removes them from every library and virtual folder, and removes their provider placements. This cannot be undone from Decypharr.`)) {
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
            window.createToast(`Deleted ${ids.length} item(s)`, 'success');
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
