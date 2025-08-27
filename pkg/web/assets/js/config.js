// Configuration management for Decypharr
class ConfigManager {
    constructor() {
        this.debridCount = 0;
        this.arrCount = 0;
        this.debridDirectoryCounts = {};
        this.directoryFilterCounts = {};

        this.refs = {
            configForm: document.getElementById('configForm'),
            loadingOverlay: document.getElementById('loadingOverlay'),
            debridConfigs: document.getElementById('debridConfigs'),
            arrConfigs: document.getElementById('arrConfigs'),
            addDebridBtn: document.getElementById('addDebridBtn'),
            addArrBtn: document.getElementById('addArrBtn')
        };

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadConfiguration();
        this.setupMagnetHandler();
        this.checkIncompleteConfig();
    }

    checkIncompleteConfig() {
        const urlParams = new URLSearchParams(window.location.search);
        if (urlParams.has('inco')) {
            const errMsg = urlParams.get('inco');
            window.decypharrUtils.createToast(`Incomplete configuration: ${errMsg}`, 'warning');
        }
    }

    bindEvents() {
        // Form submission
        this.refs.configForm.addEventListener('submit', (e) => this.saveConfiguration(e));

        // Add buttons
        this.refs.addDebridBtn.addEventListener('click', () => this.addDebridConfig());
        this.refs.addArrBtn.addEventListener('click', () => this.addArrConfig());

        // WebDAV toggle handlers
        document.addEventListener('change', (e) => {
            if (e.target.classList.contains('useWebdav')) {
                this.toggleWebDAVSection(e.target);
            }
        });
    }

    async loadConfiguration() {
        try {
            const response = await window.decypharrUtils.fetcher('/api/config');
            if (!response.ok) {
                throw new Error('Failed to load configuration');
            }

            const config = await response.json();
            this.populateForm(config);

        } catch (error) {
            console.error('Error loading configuration:', error);
            window.decypharrUtils.createToast('Error loading configuration', 'error');
        }
    }

    populateForm(config) {
        // Load general settings
        this.populateGeneralSettings(config);

        // Load debrid configs
        if (config.debrids && Array.isArray(config.debrids)) {
            config.debrids.forEach(debrid => this.addDebridConfig(debrid));
        }

        // Load qBittorrent config
        this.populateQBittorrentSettings(config.qbittorrent);

        // Load Arr configs
        if (config.arrs && Array.isArray(config.arrs)) {
            config.arrs.forEach(arr => this.addArrConfig(arr));
        }

        // Load repair config
        this.populateRepairSettings(config.repair);

        // Load rclone config
        this.populateRcloneSettings(config.rclone);

        // Load API token info
        this.populateAPIToken(config);
    }

    populateGeneralSettings(config) {
        const fields = [
            'log_level', 'url_base', 'bind_address', 'port',
            'discord_webhook_url', 'min_file_size', 'max_file_size', 'remove_stalled_after'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="${field}"]`);
            if (element && config[field] !== undefined) {
                element.value = config[field];
            }
        });

        // Handle allowed file types (array)
        if (config.allowed_file_types && Array.isArray(config.allowed_file_types)) {
            document.querySelector('[name="allowed_file_types"]').value = config.allowed_file_types.join(', ');
        }
    }

    populateQBittorrentSettings(qbitConfig) {
        if (!qbitConfig) return;

        const fields = ['download_folder', 'refresh_interval', 'max_downloads', 'skip_pre_cache'];

        fields.forEach(field => {
            const element = document.querySelector(`[name="qbit.${field}"]`);
            if (element && qbitConfig[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = qbitConfig[field];
                } else {
                    element.value = qbitConfig[field];
                }
            }
        });
    }

    populateRepairSettings(repairConfig) {
        if (!repairConfig) return;

        const fields = ['enabled', 'interval', 'workers', 'zurg_url', 'strategy', 'use_webdav', 'auto_process'];

        fields.forEach(field => {
            const element = document.querySelector(`[name="repair.${field}"]`);
            if (element && repairConfig[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = repairConfig[field];
                } else {
                    element.value = repairConfig[field];
                }
            }
        });
    }

    populateRcloneSettings(rcloneConfig) {
        if (!rcloneConfig) return;

        const fields = [
            'enabled', 'rc_port', 'mount_path', 'cache_dir', 'transfers', 'vfs_cache_mode', 'vfs_cache_max_size', 'vfs_cache_max_age',
            'vfs_cache_poll_interval', 'vfs_read_chunk_size', 'vfs_read_chunk_size_limit', 'buffer_size',
            'uid', 'gid', 'vfs_read_ahead', 'attr_timeout', 'dir_cache_time', 'poll_interval', 'umask',
            'no_modtime', 'no_checksum', 'log_level', 'vfs_cache_min_free_space', 'vfs_fast_fingerprint', 'vfs_read_chunk_streams',
            'async_read', 'use_mmap'
        ];

        fields.forEach(field => {
            const element = document.querySelector(`[name="rclone.${field}"]`);
            if (element && rcloneConfig[field] !== undefined) {
                if (element.type === 'checkbox') {
                    element.checked = rcloneConfig[field];
                } else {
                    element.value = rcloneConfig[field];
                }
            }
        });
    }

    addDebridConfig(data = {}) {
        const debridHtml = this.getDebridTemplate(this.debridCount, data);
        this.refs.debridConfigs.insertAdjacentHTML('beforeend', debridHtml);

        // Initialize WebDAV toggle for this debrid
        const newDebrid = this.refs.debridConfigs.lastElementChild;
        const webdavToggle = newDebrid.querySelector('.useWebdav');

        if (data.use_webdav) {
            this.toggleWebDAVSection(webdavToggle, true);
        }

        // Populate data if provided
        if (Object.keys(data).length > 0) {
            this.populateDebridData(this.debridCount, data);
        }

        // Initialize directory management
        this.debridDirectoryCounts[this.debridCount] = 0;

        // Add directories if they exist
        if (data.directories) {
            Object.entries(data.directories).forEach(([dirName, dirData]) => {
                const dirIndex = this.addDirectory(this.debridCount, { name: dirName, ...dirData });

                // Add filters if available
                if (dirData.filters) {
                    Object.entries(dirData.filters).forEach(([filterType, filterValue]) => {
                        this.addFilter(this.debridCount, dirIndex, filterType, filterValue);
                    });
                }
            });
        }

        this.debridCount++;
    }

    populateDebridData(index, data) {
        Object.entries(data).forEach(([key, value]) => {
            const input = document.querySelector(`[name="debrid[${index}].${key}"]`);
            if (input) {
                if (input.type === 'checkbox') {
                    input.checked = value;
                } else if (key === 'download_api_keys' && Array.isArray(value)) {
                    input.value = value.join('\n');
                    // Apply masking to populated textarea
                    if (input.tagName.toLowerCase() === 'textarea') {
                        input.style.webkitTextSecurity = 'disc';
                        input.style.textSecurity = 'disc';
                        input.setAttribute('data-password-visible', 'false');
                    }
                } else {
                    input.value = value;
                }
            }
        });
    }

    getDebridTemplate(index, data = {}) {
        return `
        <div class="card bg-base-100 border border-base-300 shadow-sm debrid-config" data-index="${index}">
            <div class="card-body">
                <div class="flex justify-between items-start mb-4">
                    <h3 class="card-title text-lg">
                        <i class="bi bi-cloud mr-2 text-secondary"></i>
                        Debrid Service #${index + 1}
                    </h3>
                    <button type="button" class="btn btn-error btn-sm" onclick="this.closest('.debrid-config').remove();">
                        <i class="bi bi-trash"></i>
                    </button>
                </div>
                <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
                        <div class="form-control">
                            <label class="label" for="debrid[${index}].name">
                                <span class="label-text font-medium">Service Type</span>
                            </label>
                            <select class="select select-bordered" name="debrid[${index}].name" id="debrid[${index}].name" required>
                                <option value="realdebrid">Real Debrid</option>
                                <option value="alldebrid">AllDebrid</option>
                                <option value="debridlink">Debrid Link</option>
                                <option value="torbox">Torbox</option>
                            </select>
                        </div>

                        <div class="form-control">
                            <label class="label" for="debrid[${index}].api_key">
                                <span class="label-text font-medium">API Key</span>
                            </label>
                            <div class="password-toggle-container">
                                <input type="password" class="input input-bordered input-has-toggle" 
                                       name="debrid[${index}].api_key" id="debrid[${index}].api_key" required>
                                <button type="button" class="password-toggle-btn">
                                    <i class="bi bi-eye" id="debrid[${index}].api_key_icon"></i>
                                </button>
                            </div>
                            <div class="label">
                                <span class="label-text-alt">API key for the debrid service</span>
                            </div>
                        </div>
                </div>

                <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
                    <div class="flex flex-col">
                        <div class="form-control flex-1">
                            <label class="label" for="debrid[${index}].download_api_keys">
                                <span class="label-text font-medium">Download API Keys</span>
                            </label>
                            <div class="password-toggle-container">
                                <textarea class="textarea textarea-bordered has-toggle font-mono h-full min-h-[200px]" 
                                          name="debrid[${index}].download_api_keys" 
                                          id="debrid[${index}].download_api_keys" 
                                          placeholder="Multiple API keys for download (one per line). If empty, main API key will be used."></textarea>
                                <button type="button" class="password-toggle-btn textarea-toggle">
                                    <i class="bi bi-eye" id="debrid[${index}].download_api_keys_icon"></i>
                                </button>
                            </div>
                            <div class="label">
                                <span class="label-text-alt">Multiple API keys for downloads - leave empty to use main API key</span>
                            </div>
                        </div>
                    </div>
                    <div class="space-y-4">
                    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                        <div class="form-control">
                            <label class="label" for="debrid[${index}].folder">
                                <span class="label-text font-medium">Mount/Rclone Folder</span>
                            </label>
                            <input type="text" class="input input-bordered" 
                                   name="debrid[${index}].folder" id="debrid[${index}].folder" 
                                   placeholder="/mnt/remote/realdebrid/__all__" required>
                            <div class="label">
                                <span class="label-text-alt">Path where debrid files are mounted</span>
                            </div>
                        </div>
                        <div class="form-control">
                              <label class="label" for="debrid[${index}].rclone_mount_path">
                                  <span class="label-text font-medium">Custom Rclone Mount Path</span>
                                  <span class="badge badge-ghost badge-sm">Optional</span>
                              </label>
                              <input type="text" class="input input-bordered" 
                                     name="debrid[${index}].rclone_mount_path" id="debrid[${index}].rclone_mount_path" 
                                     placeholder="/custom/mount/path (leave empty for global mount path)">
                              <div class="label">
                                  <span class="label-text-alt">Custom mount path for this debrid service. If empty, uses global rclone mount path.</span>
                              </div>
                        </div>
                        
                    </div>
                    <div class="grid grid-cols-2 lg:grid-cols-3 gap-3">
                        <div class="form-control">
                            <label class="label" for="debrid[${index}].rate_limit">
                                <span class="label-text font-medium">Rate Limit</span>
                            </label>
                            <input type="text" class="input input-bordered" 
                                   name="debrid[${index}].rate_limit" id="debrid[${index}].rate_limit" 
                                   placeholder="250/minute" value="250/minute">
                            <div class="label">
                                <span class="label-text-alt">API rate limit for this service</span>
                            </div>
                        </div>
                        <div class="form-control">
                            <label class="label" for="debrid[${index}].proxy">
                                <span class="label-text font-medium">Proxy</span>
                            </label>
                            <input type="text" class="input input-bordered" 
                                   name="debrid[${index}].proxy" id="debrid[${index}].proxy" 
                                   placeholder="socks4, socks5, https proxy">
                            <div class="label">
                                <span class="label-text-alt">This proxy is used for this debrid account</span>
                            </div>
                        </div>
                        <div class="form-control">
                            <label class="label" for="debrid[${index}].minimum_free_slot">
                                <span class="label-text font-medium">Minimum Free Slot</span>
                            </label>
                            <input type="number" class="input input-bordered" 
                                   name="debrid[${index}].minimum_free_slot" id="debrid[${index}].minimum_free_slot" 
                                   placeholder="1" value="1">
                            <div class="label">
                                <span class="label-text-alt">Minimum free slot for this debrid</span>
                            </div>
                        </div>
                    </div>
                        
                    </div>
                </div>

                <div class="grid grid-cols-2 lg:grid-cols-4 gap-4 mt-6">
                    <div class="form-control">
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox useWebdav" 
                                   name="debrid[${index}].use_webdav" id="debrid[${index}].use_webdav">
                            <span class="label-text font-medium">Enable WebDAV</span>
                        </label>
                        <div class="label">
                            <span class="label-text-alt">Create internal WebDAV server</span>
                        </div>
                    </div>

                    <div class="form-control">
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox" 
                                   name="debrid[${index}].download_uncached" id="debrid[${index}].download_uncached">
                            <span class="label-text font-medium">Download Uncached</span>
                        </label>
                        <div class="label">
                            <span class="label-text-alt">Download uncached files</span>
                        </div>
                    </div>

                    <div class="form-control">
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox" 
                                   name="debrid[${index}].add_samples" id="debrid[${index}].add_samples">
                            <span class="label-text font-medium">Add Samples</span>
                        </label>
                        <div class="label">
                            <span class="label-text-alt">Include sample files</span>
                        </div>
                    </div>

                    <div class="form-control">
                        <label class="label cursor-pointer justify-start gap-2">
                            <input type="checkbox" class="checkbox" 
                                   name="debrid[${index}].unpack_rar" id="debrid[${index}].unpack_rar">
                            <span class="label-text font-medium">Unpack RAR</span>
                        </label>
                        <div class="label">
                            <span class="label-text-alt">Preprocess RAR files</span>
                        </div>
                    </div>
                </div>

                <div class="webdav-section hidden mt-6" id="webdav-section-${index}">
                    <div class="divider">
                        <span class="text-lg font-semibold">WebDAV Settings</span>
                    </div>
                    
                    <div class="grid grid-cols-1 lg:grid-cols-4 gap-6">
                        <div class="form-control">
                                <label class="label" for="debrid[${index}].torrents_refresh_interval">
                                    <span class="label-text font-medium">Torrents Refresh Interval</span>
                                </label>
                                <input type="text" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].torrents_refresh_interval" 
                                       id="debrid[${index}].torrents_refresh_interval" 
                                       placeholder="15s" value="15s">
                                <div class="label">
                                    <span class="label-text-alt">How often to refresh torrents list</span>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label" for="debrid[${index}].download_links_refresh_interval">
                                    <span class="label-text font-medium">Links Refresh Interval</span>
                                </label>
                                <input type="text" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].download_links_refresh_interval" 
                                       id="debrid[${index}].download_links_refresh_interval" 
                                       placeholder="40m" value="40m">
                                <div class="label">
                                    <span class="label-text-alt">How often to refresh download links</span>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label" for="debrid[${index}].auto_expire_links_after">
                                    <span class="label-text font-medium">Expire Links After</span>
                                </label>
                                <input type="text" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].auto_expire_links_after" 
                                       id="debrid[${index}].auto_expire_links_after" 
                                       placeholder="3d" value="3d">
                                <div class="label">
                                    <span class="label-text-alt">How long to keep links in WebDAV</span>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label" for="debrid[${index}].workers">
                                    <span class="label-text font-medium">Workers</span>
                                </label>
                                <input type="number" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].workers" id="debrid[${index}].workers" 
                                       placeholder="50">
                                <div class="label">
                                    <span class="label-text-alt">Number of concurrent workers</span>
                                </div>
                            </div>
                            
                            <div class="form-control">
                                <label class="label" for="debrid[${index}].folder_naming">
                                    <span class="label-text font-medium">Folder Naming</span>
                                </label>
                                <select class="select select-bordered webdav-field" 
                                        name="debrid[${index}].folder_naming" id="debrid[${index}].folder_naming">
                                    <option value="original_no_ext" selected>Original name (No Extension)</option>
                                    <option value="original">Original name</option>
                                    <option value="filename">File name</option>
                                    <option value="filename_no_ext">File name (No Extension)</option>
                                    <option value="id">Use ID</option>
                                    <option value="infohash">Use Infohash</option>
                                </select>
                                <div class="label">
                                    <span class="label-text-alt">How to name torrent directories</span>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label" for="debrid[${index}].rc_url">
                                    <span class="label-text font-medium">Rclone RC URL</span>
                                </label>
                                <input type="url" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].rc_url" id="debrid[${index}].rc_url" 
                                       placeholder="http://localhost:9990">
                                <div class="label">
                                    <span class="label-text-alt">Rclone RC URL (speeds up imports)</span>
                                </div>
                            </div>

                            <div class="form-control">
                                <label class="label" for="debrid[${index}].rc_refresh_dirs">
                                    <span class="label-text font-medium">RC Refresh Directories</span>
                                </label>
                                <input type="text" class="input input-bordered webdav-field" 
                                       name="debrid[${index}].rc_refresh_dirs" id="debrid[${index}].rc_refresh_dirs" 
                                       placeholder="__all__, torrents">
                                <div class="label">
                                    <span class="label-text-alt">Comma-separated directory list</span>
                                </div>
                            </div>
                            <div class="form-control">
                                    <label class="label" for="debrid[${index}].rc_user">
                                        <span class="label-text font-medium">RC User</span>
                                    </label>
                                    <input type="text" class="input input-bordered webdav-field" 
                                           name="debrid[${index}].rc_user" id="debrid[${index}].rc_user">
                                </div>

                                <div class="form-control">
                                    <label class="label" for="debrid[${index}].rc_pass">
                                        <span class="label-text font-medium">RC Password</span>
                                    </label>
                                    <div class="password-toggle-container">
                                        <input type="password" class="input input-bordered webdav-field input-has-toggle" 
                                               name="debrid[${index}].rc_pass" id="debrid[${index}].rc_pass">
                                        <button type="button" class="password-toggle-btn">
                                            <i class="bi bi-eye" id="debrid[${index}].rc_pass_icon"></i>
                                        </button>
                                    </div>
                                </div>

                        <div class="form-control">
                                <label class="label cursor-pointer justify-start gap-2">
                                    <input type="checkbox" class="checkbox webdav-field" 
                                           name="debrid[${index}].serve_from_rclone" id="debrid[${index}].serve_from_rclone">
                                    <span class="label-text font-medium">Serve From Rclone</span>
                                </label>
                                <div class="label">
                                    <span class="label-text-alt">Let Rclone handle serving/streaming</span>
                                </div>
                            </div>
                    </div>

                    <div class="mt-6">
                        <div class="flex justify-between items-center mb-4">
                            <h4 class="text-lg font-semibold">Virtual Directories</h4>
                            <button type="button" class="btn btn-secondary btn-sm" onclick="configManager.addDirectory(${index});">
                                <i class="bi bi-plus mr-2"></i>Add Directory
                            </button>
                        </div>
                        <p class="text-sm text-base-content/70 mb-4">Create virtual directories with filters to organize your content</p>
                        <div class="directories-container space-y-4" id="debrid[${index}].directories">
                        </div>
                    </div>
                </div>
            </div>
        </div>
    `;
    }

    toggleWebDAVSection(toggle, forceShow = false) {
        const debridCard = toggle.closest('.debrid-config');
        const index = debridCard.dataset.index;
        const webdavSection = debridCard.querySelector(`#webdav-section-${index}`);
        const webdavFields = webdavSection.querySelectorAll('.webdav-field');

        if (toggle.checked || forceShow) {
            webdavSection.classList.remove('hidden');
        } else {
            webdavSection.classList.add('hidden');
            // Remove required attributes
            webdavFields.forEach(field => field.required = false);
        }
    }

    addDirectory(debridIndex, data = {}) {
        if (!this.debridDirectoryCounts[debridIndex]) {
            this.debridDirectoryCounts[debridIndex] = 0;
        }

        const dirIndex = this.debridDirectoryCounts[debridIndex];
        const container = document.getElementById(`debrid[${debridIndex}].directories`);

        const directoryHtml = this.getDirectoryTemplate(debridIndex, dirIndex);
        container.insertAdjacentHTML('beforeend', directoryHtml);

        // Set up tracking for filters in this directory
        const dirKey = `${debridIndex}-${dirIndex}`;
        this.directoryFilterCounts[dirKey] = 0;

        // Fill with directory name if provided
        if (data.name) {
            const nameInput = document.querySelector(`[name="debrid[${debridIndex}].directory[${dirIndex}].name"]`);
            if (nameInput) nameInput.value = data.name;
        }

        this.debridDirectoryCounts[debridIndex]++;
        return dirIndex;
    }

    getDirectoryTemplate(debridIndex, dirIndex) {
        return `
            <div class="card bg-base-200 border border-base-300 directory-item">
                <div class="card-body">
                    <div class="flex justify-between items-start mb-4">
                        <h5 class="text-lg font-medium">Virtual Directory</h5>
                        <button type="button" class="btn btn-error btn-xs" onclick="this.closest('.directory-item').remove();">
                            <i class="bi bi-trash"></i>
                        </button>
                    </div>

                    <div class="form-control mb-4">
                        <label class="label">
                            <span class="label-text font-medium">Directory Name</span>
                        </label>
                        <input type="text" class="input input-bordered webdav-field"
                               name="debrid[${debridIndex}].directory[${dirIndex}].name"
                               placeholder="Movies, TV Shows, Collections, etc.">
                    </div>

                    <div class="space-y-4">
                        <div class="flex justify-between items-center">
                            <h6 class="font-medium flex items-center">
                                Filters
                                <button type="button" class="btn btn-ghost btn-xs ml-2" onclick="configManager.showFilterHelp();">
                                    <i class="bi bi-question-circle"></i>
                                </button>
                            </h6>
                        </div>

                        <div class="filters-container space-y-2" id="debrid[${debridIndex}].directory[${dirIndex}].filters">
                        </div>

                        <div class="flex flex-wrap gap-2">
                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-plus mr-1"></i>Text Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'include');">Include</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'exclude');">Exclude</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'starts_with');">Starts With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_starts_with');">Not Starts With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'ends_with');">Ends With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_ends_with');">Not Ends With</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'exact_match');">Exact Match</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_exact_match');">Not Exact Match</a></li>
                                </ul>
                            </div>

                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-code mr-1"></i>Regex Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'regex');">Regex Match</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'not_regex');">Regex Doesn't Match</a></li>
                                </ul>
                            </div>

                            <div class="dropdown">
                                <div tabindex="0" role="button" class="btn btn-outline btn-sm">
                                    <i class="bi bi-hdd mr-1"></i>Size Filter
                                    <i class="bi bi-chevron-down ml-1"></i>
                                </div>
                                <ul tabindex="0" class="dropdown-content menu bg-base-100 rounded-box z-[1] w-48 p-2 shadow">
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'size_gt');">Size Greater Than</a></li>
                                    <li><a onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'size_lt');">Size Less Than</a></li>
                                </ul>
                            </div>

                            <button type="button" class="btn btn-outline btn-sm" onclick="configManager.addFilter(${debridIndex}, ${dirIndex}, 'last_added');">
                                <i class="bi bi-clock mr-1"></i>Last Added Filter
                            </button>
                        </div>
                    </div>
                </div>
            </div>
        `;
    }

    addFilter(debridIndex, dirIndex, filterType, filterValue = "") {
        const dirKey = `${debridIndex}-${dirIndex}`;
        if (!this.directoryFilterCounts[dirKey]) {
            this.directoryFilterCounts[dirKey] = 0;
        }

        const filterIndex = this.directoryFilterCounts[dirKey];
        const container = document.getElementById(`debrid[${debridIndex}].directory[${dirIndex}].filters`);

        if (container) {
            const filterHtml = this.getFilterTemplate(debridIndex, dirIndex, filterIndex, filterType);
            container.insertAdjacentHTML('beforeend', filterHtml);

            // Set filter value if provided
            if (filterValue) {
                const valueInput = container.querySelector(`[name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].value"]`);
                if (valueInput) valueInput.value = filterValue;
            }

            this.directoryFilterCounts[dirKey]++;
        }
    }

    getFilterTemplate(debridIndex, dirIndex, filterIndex, filterType) {
        const filterConfig = this.getFilterConfig(filterType);

        return `
            <div class="filter-item flex items-center gap-3 p-3 bg-base-100 rounded-lg border border-base-300">
                <div class="badge ${filterConfig.badgeClass} badge-sm">
                    ${filterConfig.label}
                </div>
                <input type="hidden"
                       name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].type"
                       value="${filterType}">
                <div class="flex-1">
                    <input type="text" 
                           class="input input-bordered input-sm w-full webdav-field"
                           name="debrid[${debridIndex}].directory[${dirIndex}].filter[${filterIndex}].value"
                           placeholder="${filterConfig.placeholder}">
                </div>
                <button type="button" class="btn btn-error btn-xs" onclick="this.closest('.filter-item').remove();">
                    <i class="bi bi-x"></i>
                </button>
            </div>
        `;
    }

    getFilterConfig(filterType) {
        const configs = {
            'include': {
                label: 'Include',
                placeholder: 'Text that should be included in filename',
                badgeClass: 'badge-primary'
            },
            'exclude': {
                label: 'Exclude',
                placeholder: 'Text that should not be in filename',
                badgeClass: 'badge-error'
            },
            'regex': {
                label: 'Regex Match',
                placeholder: 'Regular expression pattern',
                badgeClass: 'badge-warning'
            },
            'not_regex': {
                label: 'Regex Not Match',
                placeholder: 'Regular expression pattern that should not match',
                badgeClass: 'badge-error'
            },
            'exact_match': {
                label: 'Exact Match',
                placeholder: 'Exact text to match',
                badgeClass: 'badge-primary'
            },
            'not_exact_match': {
                label: 'Not Exact Match',
                placeholder: 'Exact text that should not match',
                badgeClass: 'badge-error'
            },
            'starts_with': {
                label: 'Starts With',
                placeholder: 'Text that filename starts with',
                badgeClass: 'badge-primary'
            },
            'not_starts_with': {
                label: 'Not Starts With',
                placeholder: 'Text that filename should not start with',
                badgeClass: 'badge-error'
            },
            'ends_with': {
                label: 'Ends With',
                placeholder: 'Text that filename ends with',
                badgeClass: 'badge-primary'
            },
            'not_ends_with': {
                label: 'Not Ends With',
                placeholder: 'Text that filename should not end with',
                badgeClass: 'badge-error'
            },
            'size_gt': {
                label: 'Size Greater Than',
                placeholder: 'Size in bytes, KB, MB, GB (e.g. 700MB)',
                badgeClass: 'badge-success'
            },
            'size_lt': {
                label: 'Size Less Than',
                placeholder: 'Size in bytes, KB, MB, GB (e.g. 700MB)',
                badgeClass: 'badge-warning'
            },
            'last_added': {
                label: 'Added in the last',
                placeholder: 'Time duration (e.g. 24h, 7d, 30d)',
                badgeClass: 'badge-info'
            }
        };

        return configs[filterType] || {
            label: filterType.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase()),
            placeholder: 'Filter value',
            badgeClass: 'badge-ghost'
        };
    }

    showFilterHelp() {
        // Create and show a modal with filter help
        const modal = document.createElement('dialog');
        modal.className = 'modal';
        modal.innerHTML = `
            <div class="modal-box max-w-2xl">
                <form method="dialog">
                    <button class="btn btn-sm btn-circle btn-ghost absolute right-2 top-2">✕</button>
                </form>
                <h3 class="font-bold text-lg mb-4">Directory Filter Types</h3>
                <div class="space-y-4">
                    <div>
                        <h4 class="font-semibold text-primary">Text Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Include/Exclude:</strong> Simple text inclusion/exclusion</li>
                            <li><strong>Starts/Ends With:</strong> Matches beginning or end of filename</li>
                            <li><strong>Exact Match:</strong> Match the entire filename</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-warning">Regex Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Regex:</strong> Use regular expressions for complex patterns</li>
                            <li>Example: <code>.*\\.mkv$</code> matches files ending with .mkv</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-success">Size Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Size Greater/Less Than:</strong> Filter by file size</li>
                            <li>Examples: 1GB, 500MB, 2.5GB</li>
                        </ul>
                    </div>
                    <div>
                        <h4 class="font-semibold text-info">Time Filters</h4>
                        <ul class="list-disc list-inside text-sm space-y-1 ml-4">
                            <li><strong>Last Added:</strong> Show only recently added content</li>
                            <li>Examples: 24h, 7d, 30d</li>
                        </ul>
                    </div>
                    <div class="alert alert-info">
                        <i class="bi bi-info-circle"></i>
                        <span>Negative filters (Not...) will exclude matches instead of including them.</span>
                    </div>
                </div>
            </div>
        `;

        document.body.appendChild(modal);
        modal.showModal();

        // Remove modal when closed
        modal.addEventListener('close', () => {
            document.body.removeChild(modal);
        });
    }

    addArrConfig(data = {}) {
        const arrHtml = this.getArrTemplate(this.arrCount, data);
        this.refs.arrConfigs.insertAdjacentHTML('beforeend', arrHtml);

        // Populate data if provided
        if (Object.keys(data).length > 0) {
            this.populateArrData(this.arrCount, data);
        }

        this.arrCount++;
    }

    populateArrData(index, data) {
        Object.entries(data).forEach(([key, value]) => {
            const input = document.querySelector(`[name="arr[${index}].${key}"]`);
            if (input) {
                if (input.type === 'checkbox') {
                    input.checked = value;
                } else {
                    input.value = value;
                }
            }
        });
    }

    getArrTemplate(index, data = {}) {
        const isAutoDetected = data.source === 'auto';

        return `
            <div class="card bg-base-100 border border-base-300 shadow-sm arr-config ${isAutoDetected ? 'border-info' : ''}" data-index="${index}">
                <div class="card-body">
                    <div class="flex justify-between items-start mb-4">
                        <h3 class="card-title text-lg">
                            <i class="bi bi-collection mr-2 text-warning"></i>
                            Arr Service #${index + 1}
                            ${isAutoDetected ? '<div class="badge badge-info badge-sm ml-2">Auto-detected</div>' : ''}
                        </h3>
                        ${!isAutoDetected ? `
                            <button type="button" class="btn btn-error btn-sm" onclick="this.closest('.arr-config').remove();">
                                <i class="bi bi-trash"></i>
                            </button>
                        ` : ''}
                    </div>

                    <input type="hidden" name="arr[${index}].source" value="${data.source || ''}">

                    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
                        <div class="form-control">
                            <label class="label" for="arr[${index}].name">
                                <span class="label-text font-medium">Service Name</span>
                            </label>
                            <input type="text" class="input input-bordered ${isAutoDetected ? 'input-disabled' : ''}" 
                                   name="arr[${index}].name" id="arr[${index}].name" 
                                   ${isAutoDetected ? 'readonly' : 'required'} 
                                   placeholder="sonarr, radarr, etc.">
                        </div>

                        <div class="form-control">
                            <label class="label" for="arr[${index}].host">
                                <span class="label-text font-medium">Host URL</span>
                            </label>
                            <input type="url" class="input input-bordered ${isAutoDetected ? 'input-disabled' : ''}" 
                                   name="arr[${index}].host" id="arr[${index}].host" 
                                   ${isAutoDetected ? 'readonly' : 'required'} 
                                   placeholder="http://localhost:8989">
                        </div>

                        <div class="form-control">
                            <label class="label" for="arr[${index}].token">
                                <span class="label-text font-medium">API Token</span>
                            </label>
                            <div class="password-toggle-container">
                                <input type="password" class="input input-bordered input-has-toggle ${isAutoDetected ? 'input-disabled' : ''}" 
                                       name="arr[${index}].token" id="arr[${index}].token" 
                                       ${isAutoDetected ? 'readonly' : 'required'}>
                                <button type="button" class="password-toggle-btn ${isAutoDetected ? 'opacity-50 cursor-not-allowed' : ''}"
                                        ${isAutoDetected ? 'disabled' : ''}>
                                    <i class="bi bi-eye" id="arr[${index}].token_icon"></i>
                                </button>
                            </div>
                        </div>
                        
                        <div class="form-control">
                            <label class="label" for="arr[${index}].selected_debrid">
                                <span class="label-text font-medium">Preferred Debrid Service</span>
                            </label>
                            <select class="select select-bordered" name="arr[${index}].selected_debrid" id="arr[${index}].selected_debrid">
                                <option value="" selected>Auto-select</option>
                                <option value="realdebrid">Real Debrid</option>
                                <option value="alldebrid">AllDebrid</option>
                                <option value="debridlink">Debrid Link</option>
                                <option value="torbox">Torbox</option>
                            </select>
                            <div class="label">
                                <span class="label-text-alt">Which debrid service this Arr should prefer</span>
                            </div>
                        </div>
                    </div>

                    <div class="grid grid-cols-3 gap-4">
                        <div class="form-control">
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm" 
                                       name="arr[${index}].cleanup" id="arr[${index}].cleanup">
                                <span class="label-text text-sm">Cleanup Queue</span>
                            </label>
                        </div>

                        <div class="form-control">
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm" 
                                       name="arr[${index}].skip_repair" id="arr[${index}].skip_repair">
                                <span class="label-text text-sm">Skip Repair</span>
                            </label>
                        </div>

                        <div class="form-control">
                            <label class="label cursor-pointer justify-start gap-2">
                                <input type="checkbox" class="checkbox checkbox-sm" 
                                       name="arr[${index}].download_uncached" id="arr[${index}].download_uncached">
                                <span class="label-text text-sm">Download Uncached</span>
                            </label>
                        </div>
                    </div>
                </div>
            </div>
        `;
    }

    async saveConfiguration(e) {
        e.preventDefault();

        // Show loading overlay
        this.refs.loadingOverlay.classList.remove('hidden');

        try {
            const config = this.collectFormData();

            // Validate configuration
            const validation = this.validateConfiguration(config);
            if (!validation.valid) {
                throw new Error(validation.errors.join('\n'));
            }

            const response = await window.decypharrUtils.fetcher('/api/config', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(config)
            });

            if (!response.ok) {
                const errorText = await response.text();
                throw new Error(errorText || 'Failed to save configuration');
            }

            window.decypharrUtils.createToast('Configuration saved successfully! Services are restarting...', 'success');

            // Reload page after a delay to allow services to restart
            setTimeout(() => {
                window.location.reload();
            }, 2000);

        } catch (error) {
            console.error('Error saving configuration:', error);
            window.decypharrUtils.createToast(`Error saving configuration: ${error.message}`, 'error');
            this.refs.loadingOverlay.classList.add('hidden');
        }
    }

    validateConfiguration(config) {
        const errors = [];

        // Validate debrid services
        config.debrids.forEach((debrid, index) => {
            if (!debrid.name || !debrid.api_key || !debrid.folder) {
                errors.push(`Debrid service #${index + 1}: Name, API key, and folder are required`);
            }
        });

        // Validate Arr services
        config.arrs.forEach((arr, index) => {
            if (!arr.name || !arr.host) {
                errors.push(`Arr service #${index + 1}: Name and host are required`);
            }

            if (arr.host && !this.isValidUrl(arr.host)) {
                errors.push(`Arr service #${index + 1}: Invalid host URL format`);
            }
        });

        // Validate repair settings
        if (config.repair.enabled) {
            if (!config.repair.interval) {
                errors.push('Repair interval is required when repair is enabled');
            }
        }

        if (config.rclone.enabled && config.rclone.mount_path === '') {
            errors.push('Rclone mount path is required when Rclone is enabled');
        }

        return {
            valid: errors.length === 0,
            errors
        };
    }

    isValidUrl(string) {
        try {
            new URL(string);
            return true;
        } catch (_) {
            return false;
        }
    }

    collectFormData() {
        return {
            // General settings
            log_level: document.getElementById('log-level').value,
            url_base: document.getElementById('urlBase').value,
            bind_address: document.getElementById('bindAddress').value,
            port: document.getElementById('port').value ? document.getElementById('port').value : null,
            discord_webhook_url: document.getElementById('discordWebhookUrl').value,
            allowed_file_types: document.getElementById('allowedExtensions').value
                .split(',').map(ext => ext.trim()).filter(Boolean),
            min_file_size: document.getElementById('minFileSize').value,
            max_file_size: document.getElementById('maxFileSize').value,
            remove_stalled_after: document.getElementById('removeStalledAfter').value,
            callback_url: document.getElementById('callbackUrl').value,

            // Debrid configurations
            debrids: this.collectDebridConfigs(),

            // QBittorrent configuration
            qbittorrent: this.collectQBittorrentConfig(),

            // Arr configurations
            arrs: this.collectArrConfigs(),

            // Repair configuration
            repair: this.collectRepairConfig(),

            // Rclone configuration
            rclone: this.collectRcloneConfig()
        };
    }

    collectDebridConfigs() {
        const debrids = [];

        for (let i = 0; i < this.debridCount; i++) {
            const nameEl = document.querySelector(`[name="debrid[${i}].name"]`);
            if (!nameEl || !nameEl.closest('.debrid-config')) continue;

            const debrid = {
                name: nameEl.value,
                api_key: document.querySelector(`[name="debrid[${i}].api_key"]`).value,
                folder: document.querySelector(`[name="debrid[${i}].folder"]`).value,
                rate_limit: document.querySelector(`[name="debrid[${i}].rate_limit"]`).value,
                minimum_free_slot: parseInt(document.querySelector(`[name="debrid[${i}].minimum_free_slot"]`).value) || 0,
                rclone_mount_path: document.querySelector(`[name="debrid[${i}].rclone_mount_path"]`).value,
                proxy: document.querySelector(`[name="debrid[${i}].proxy"]`).value,
                download_uncached: document.querySelector(`[name="debrid[${i}].download_uncached"]`).checked,
                unpack_rar: document.querySelector(`[name="debrid[${i}].unpack_rar"]`).checked,
                add_samples: document.querySelector(`[name="debrid[${i}].add_samples"]`).checked,
                use_webdav: document.querySelector(`[name="debrid[${i}].use_webdav"]`).checked
            };

            // Handle download API keys
            const downloadKeysTextarea = document.querySelector(`[name="debrid[${i}].download_api_keys"]`);
            if (downloadKeysTextarea && downloadKeysTextarea.value.trim()) {
                debrid.download_api_keys = downloadKeysTextarea.value
                    .split('\n')
                    .map(key => key.trim())
                    .filter(key => key.length > 0);
            }

            // Add WebDAV specific properties if enabled
            if (debrid.use_webdav) {
                debrid.torrents_refresh_interval = document.querySelector(`[name="debrid[${i}].torrents_refresh_interval"]`).value;
                debrid.download_links_refresh_interval = document.querySelector(`[name="debrid[${i}].download_links_refresh_interval"]`).value;
                debrid.auto_expire_links_after = document.querySelector(`[name="debrid[${i}].auto_expire_links_after"]`).value;
                debrid.folder_naming = document.querySelector(`[name="debrid[${i}].folder_naming"]`).value;
                debrid.workers = parseInt(document.querySelector(`[name="debrid[${i}].workers"]`).value);
                debrid.rc_url = document.querySelector(`[name="debrid[${i}].rc_url"]`).value;
                debrid.rc_user = document.querySelector(`[name="debrid[${i}].rc_user"]`).value;
                debrid.rc_pass = document.querySelector(`[name="debrid[${i}].rc_pass"]`).value;
                debrid.rc_refresh_dirs = document.querySelector(`[name="debrid[${i}].rc_refresh_dirs"]`).value;
                debrid.serve_from_rclone = document.querySelector(`[name="debrid[${i}].serve_from_rclone"]`).checked;

                // Collect virtual directories
                debrid.directories = {};
                const dirCount = this.debridDirectoryCounts[i] || 0;

                for (let j = 0; j < dirCount; j++) {
                    const nameInput = document.querySelector(`[name="debrid[${i}].directory[${j}].name"]`);
                    if (nameInput && nameInput.value && nameInput.closest('.directory-item')) {
                        const dirName = nameInput.value;
                        debrid.directories[dirName] = { filters: {} };

                        // Collect filters for this directory
                        const dirKey = `${i}-${j}`;
                        const filterCount = this.directoryFilterCounts[dirKey] || 0;

                        for (let k = 0; k < filterCount; k++) {
                            const filterTypeInput = document.querySelector(`[name="debrid[${i}].directory[${j}].filter[${k}].type"]`);
                            const filterValueInput = document.querySelector(`[name="debrid[${i}].directory[${j}].filter[${k}].value"]`);

                            if (filterTypeInput && filterValueInput && filterValueInput.value && filterValueInput.closest('.filter-item')) {
                                const filterType = filterTypeInput.value;
                                debrid.directories[dirName].filters[filterType] = filterValueInput.value;
                            }
                        }
                    }
                }
            }

            if (debrid.name && debrid.api_key) {
                debrids.push(debrid);
            }
        }

        return debrids;
    }

    collectQBittorrentConfig() {
        return {
            download_folder: document.querySelector('[name="qbit.download_folder"]').value,
            refresh_interval: parseInt(document.querySelector('[name="qbit.refresh_interval"]').value) || 30,
            max_downloads: parseInt(document.querySelector('[name="qbit.max_downloads"]').value) || 0,
            skip_pre_cache: document.querySelector('[name="qbit.skip_pre_cache"]').checked
        };
    }

    collectArrConfigs() {
        const arrs = [];

        for (let i = 0; i < this.arrCount; i++) {
            const nameEl = document.querySelector(`[name="arr[${i}].name"]`);
            if (!nameEl || !nameEl.closest('.arr-config')) continue;

            const arr = {
                name: nameEl.value,
                host: document.querySelector(`[name="arr[${i}].host"]`).value,
                token: document.querySelector(`[name="arr[${i}].token"]`).value,
                cleanup: document.querySelector(`[name="arr[${i}].cleanup"]`).checked,
                skip_repair: document.querySelector(`[name="arr[${i}].skip_repair"]`).checked,
                download_uncached: document.querySelector(`[name="arr[${i}].download_uncached"]`).checked,
                selected_debrid: document.querySelector(`[name="arr[${i}].selected_debrid"]`).value,
                source: document.querySelector(`[name="arr[${i}].source"]`).value
            };

            if (arr.name && arr.host) {
                arrs.push(arr);
            }
        }

        return arrs;
    }

    collectRepairConfig() {
        return {
            enabled: document.querySelector('[name="repair.enabled"]').checked,
            interval: document.querySelector('[name="repair.interval"]').value,
            zurg_url: document.querySelector('[name="repair.zurg_url"]').value,
            strategy: document.querySelector('[name="repair.strategy"]').value,
            workers: parseInt(document.querySelector('[name="repair.workers"]').value) || 1,
            use_webdav: document.querySelector('[name="repair.use_webdav"]').checked,
            auto_process: document.querySelector('[name="repair.auto_process"]').checked
        };
    }

    collectRcloneConfig() {
        const getElementValue = (name, defaultValue = '') => {
            const element = document.querySelector(`[name="rclone.${name}"]`);
            if (!element) return defaultValue;
            
            if (element.type === 'checkbox') {
                return element.checked;
            } else if (element.type === 'number') {
                const val = parseInt(element.value);
                return isNaN(val) ? 0 : val;
            } else {
                return element.value || defaultValue;
            }
        };

        return {
            enabled: getElementValue('enabled', false),
            rc_port: getElementValue('rc_port', "5572"),
            mount_path: getElementValue('mount_path'),
            buffer_size: getElementValue('buffer_size'),
            cache_dir: getElementValue('cache_dir'),
            transfers: getElementValue('transfers', 8),
            vfs_cache_mode: getElementValue('vfs_cache_mode', 'off'),
            vfs_cache_max_age: getElementValue('vfs_cache_max_age', '1h'),
            vfs_cache_max_size: getElementValue('vfs_cache_max_size'),
            vfs_cache_poll_interval: getElementValue('vfs_cache_poll_interval', '1m'),
            vfs_read_chunk_size: getElementValue('vfs_read_chunk_size', '128M'),
            vfs_read_chunk_size_limit: getElementValue('vfs_read_chunk_size_limit', 'off'),
            vfs_cache_min_free_space: getElementValue('vfs_cache_min_free_space', ''),
            vfs_fast_fingerprint: getElementValue('vfs_fast_fingerprint', false),
            vfs_read_chunk_streams: getElementValue('vfs_read_chunk_streams', 0),
            use_mmap: getElementValue('use_mmap', false),
            async_read: getElementValue('async_read', true),
            uid: getElementValue('uid', 0),
            gid: getElementValue('gid', 0),
            umask: getElementValue('umask', ''),
            vfs_read_ahead: getElementValue('vfs_read_ahead', '128k'),
            attr_timeout: getElementValue('attr_timeout', '1s'),
            dir_cache_time: getElementValue('dir_cache_time', '5m'),
            no_modtime: getElementValue('no_modtime', false),
            no_checksum: getElementValue('no_checksum', false),
            log_level: getElementValue('log_level', 'INFO'),
        };
    }

    setupMagnetHandler() {
        window.registerMagnetLinkHandler = () => {
            if ('registerProtocolHandler' in navigator) {
                try {
                    navigator.registerProtocolHandler(
                        'magnet',
                        `${window.location.origin}${window.urlBase}download?magnet=%s`,
                        'Decypharr'
                    );
                    localStorage.setItem('magnetHandler', 'true');
                    const btn = document.getElementById('registerMagnetLink');
                    btn.innerHTML = '<i class="bi bi-check-circle mr-2"></i>Magnet Handler Registered';
                    btn.classList.remove('btn-primary');
                    btn.classList.add('btn-success');
                    btn.disabled = true;
                    window.decypharrUtils.createToast('Magnet link handler registered successfully');
                } catch (error) {
                    console.error('Failed to register magnet link handler:', error);
                    window.decypharrUtils.createToast('Failed to register magnet link handler', 'error');
                }
            } else {
                window.decypharrUtils.createToast('Magnet link registration not supported in this browser', 'warning');
            }
        };

        // Check if already registered
        if (localStorage.getItem('magnetHandler') === 'true') {
            const btn = document.getElementById('registerMagnetLink');
            if (btn) {
                btn.innerHTML = '<i class="bi bi-check-circle mr-2"></i>Magnet Handler Registered';
                btn.classList.remove('btn-primary');
                btn.classList.add('btn-success');
                btn.disabled = true;
            }
        }
    }

    populateAPIToken(config) {
        const tokenDisplay = document.getElementById('api-token-display');
        if (tokenDisplay) {
            tokenDisplay.value = config.api_token || '****';
        }

        // Populate username (password is not populated for security)
        const usernameField = document.getElementById('auth-username');
        if (usernameField && config.auth_username) {
            usernameField.value = config.auth_username;
        }
    }
}


