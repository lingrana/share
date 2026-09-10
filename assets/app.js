(() => {
    const csrfToken = () => document.querySelector('meta[name="csrf"]')?.content || '';

    window.postAction = function(action, data) {
        const form = document.createElement('form');
        form.method = 'POST';
        form.action = '/admin.html';
        const input = document.createElement('input');
        input.type = 'hidden';
        input.name = 'action';
        input.value = action;
        form.appendChild(input);
        const csrf = csrfToken();
        if (csrf) {
            const c = document.createElement('input');
            c.type = 'hidden';
            c.name = 'csrf';
            c.value = csrf;
            form.appendChild(c);
        }
        for (const [k, v] of Object.entries(data)) {
            const i = document.createElement('input');
            i.type = 'hidden';
            i.name = k;
            i.value = v;
            form.appendChild(i);
        }
        document.body.appendChild(form);
        form.submit();
    };
    // ==================== 主题与明暗 ====================
    // 两个独立维度：
    //   data-theme — 主题（配色/布局包），由管理员在后台统一指定，前端脚本不得改动；
    //   data-mode  — 明暗变体（light/dark），访客可切换，localStorage 记忆，默认跟随系统。
    // CSS 优先级：浅色主题块 → :root[data-mode="dark"] 全局兜底 →
    //             :root[data-theme="X"][data-mode="dark"] 主题专属暗色块。
    const modeBtns = document.querySelectorAll('.theme-toggle-btn');

    function applyMode(mode) {
        if (mode === 'dark') {
            document.documentElement.setAttribute('data-mode', 'dark');
            modeBtns.forEach(btn => {
                btn.innerHTML = '[ ☀️ 浅色 ]';
                btn.setAttribute('title', '切换到浅色模式');
            });
        } else {
            document.documentElement.setAttribute('data-mode', 'light');
            modeBtns.forEach(btn => {
                btn.innerHTML = '[ 🌙 深色 ]';
                btn.setAttribute('title', '切换到深色模式');
            });
        }
    }

    const savedMode = localStorage.getItem('mono_mode') ||
        (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
    applyMode(savedMode);

    modeBtns.forEach(btn => {
        btn.addEventListener('click', (e) => {
            e.preventDefault();
            const isDark = document.documentElement.getAttribute('data-mode') === 'dark';
            const next = isDark ? 'light' : 'dark';
            localStorage.setItem('mono_mode', next);
            applyMode(next);
        });
    });

    // 模态弹窗控制
    window.openModal = function(id) {
        const modal = document.getElementById(id);
        if (modal) {
            modal.classList.add('is-open');
            document.body.style.overflow = 'hidden';
        }
    };

    window.closeModal = function() {
        document.querySelectorAll('.modal-overlay').forEach(m => m.classList.remove('is-open'));
        document.body.style.overflow = '';
    };

    window.openNodeEditModal = function(btn) {
        document.getElementById('editNodeId').value = btn.dataset.nodeId || '';
        document.getElementById('editNodeName').value = btn.dataset.nodeName || '';
        document.getElementById('editNodeBaseUrl').value = btn.dataset.nodeBaseUrl || '';
        document.getElementById('editNodeToken').value = btn.dataset.nodeToken || '';
        document.getElementById('editNodeAntiBot').checked = btn.dataset.nodeAntiBot === '1';
        openModal('nodeEditModal');
    };

    document.querySelectorAll('.modal-overlay').forEach(modal => {
        modal.addEventListener('click', (e) => {
            if (e.target !== modal) return;
            if (modal.id === 'announcementModal') {
                closeAnnouncementModal();
            } else {
                closeModal();
            }
        });
    });

    document.addEventListener('keydown', (e) => {
        if (e.key === 'Escape') {
            const announcementModal = document.getElementById('announcementModal');
            if (announcementModal && announcementModal.classList.contains('is-open')) {
                closeAnnouncementModal();
            } else {
                closeModal();
            }
        }
    });

    window.closeAnnouncementModal = function() {
        const modal = document.getElementById('announcementModal');
        if (modal) {
            modal.classList.remove('is-open');
            document.body.style.overflow = '';
            // 向后端提交该 IP 已读标记
            try {
                const formData = new FormData();
                formData.append('action', 'read_announcement');
                const csrf = csrfToken();
                if (csrf) formData.append('csrf', csrf);
                fetch('/index.html', {
                    method: 'POST',
                    body: formData,
                    headers: { 'X-Requested-With': 'XMLHttpRequest' }
                }).catch(() => {});
            } catch (e) {}
        }
    };

    // 复制到剪贴板与纯粹黑白吐司
    window.copyText = function(text, label = 'COPIED TO CLIPBOARD') {
        if (!text) return;
        navigator.clipboard.writeText(text).then(() => {
            showMonoToast(label);
        }).catch(() => {
            const input = document.createElement('input');
            input.value = text;
            document.body.appendChild(input);
            input.select();
            document.execCommand('copy');
            document.body.removeChild(input);
            showMonoToast(label);
        });
    };

    // 极简黑白 Toast 提示
    window.showMonoToast = function(msg) {
        let toast = document.getElementById('mono-toast');
        if (!toast) {
            toast = document.createElement('div');
            toast.id = 'mono-toast';
            toast.style.cssText = `
                position: fixed;
                bottom: 30px;
                left: 50%;
                transform: translateX(-50%) translateY(20px);
                background: var(--fg);
                color: var(--bg);
                border: 1px solid var(--bg);
                padding: 0.6rem 1.4rem;
                font-family: var(--font-mono);
                font-weight: 700;
                font-size: 0.8rem;
                letter-spacing: 0.08em;
                z-index: 1000;
                opacity: 0;
                pointer-events: none;
                transition: opacity 120ms ease, transform 120ms ease;
            `;
            document.body.appendChild(toast);
        }
        toast.textContent = `[ ${msg} ]`;
        toast.style.opacity = '1';
        toast.style.transform = 'translateX(-50%) translateY(0)';
        clearTimeout(toast._timer);
        toast._timer = setTimeout(() => {
            toast.style.opacity = '0';
            toast.style.transform = 'translateX(-50%) translateY(20px)';
        }, 2000);
    };

    function detectCloudPlatform(text) {
        const t = (text || '').toLowerCase();
        if (t.includes('pan.baidu.com') || t.includes('yun.baidu.com')) return 'baidu';
        if (t.includes('pan.quark.cn') || t.includes('quark.cn')) return 'quark';
        if (t.includes('alipan.com') || t.includes('aliyundrive.com')) return 'aliyun';
        if (t.includes('lanzou') || t.includes('lanzoui.com') || t.includes('lanzoux.com')) return 'lanzou';
        if (t.includes('115.com') || t.includes('anxia.com')) return '115';
        return 'other';
    }

    function parseCloudShareInput(raw) {
        const text = (raw || '').trim();
        let url = '';
        let code = '';
        const urlMatch = text.match(/https?:\/\/[^\s]+/i);
        if (urlMatch) {
            url = urlMatch[0].replace(/[.,;）)]+$/, '');
        }
        const pwdMatch = text.match(/[?&]pwd=([^&\s]+)/i);
        const codeMatch = text.match(/提取码[:：\s]*([A-Za-z0-9]{3,8})/) || text.match(/(?:密码|口令)[:：\s]*([A-Za-z0-9]{3,8})/);
        if (pwdMatch) code = decodeURIComponent(pwdMatch[1]);
        else if (codeMatch) code = codeMatch[1];
        return { url, code, platform: detectCloudPlatform(url || text) };
    }

    function toggleUnpackPassword(row) {
        const select = row.querySelector('select[name="link_platform[]"]');
        const pwd = row.querySelector('.js-link-password');
        if (!select || !pwd) return;
        pwd.style.display = select.value === 'other' ? '' : 'none';
        if (select.value !== 'other') pwd.value = '';
    }

    function applySharePaste(row, raw) {
        const parsed = parseCloudShareInput(raw);
        const urlInput = row.querySelector('.js-link-url');
        const codeInput = row.querySelector('.js-link-code');
        const platformSelect = row.querySelector('select[name="link_platform[]"]');
        const looksLikeShareText = /提取码|复制这段内容|打开百度网盘/u.test(raw) || (parsed.url && parsed.url !== raw.trim());
        if (looksLikeShareText && parsed.url && urlInput) urlInput.value = parsed.url;
        if (parsed.code && codeInput && !codeInput.value) codeInput.value = parsed.code;
        if (platformSelect && parsed.platform !== 'other') platformSelect.value = parsed.platform;
        toggleUnpackPassword(row);
    }

    document.addEventListener('change', function (e) {
        const row = e.target.closest('.link-input-row');
        if (!row) return;
        if (e.target.matches('select[name="link_platform[]"]')) {
            toggleUnpackPassword(row);
        }
        if (e.target.matches('.js-link-url')) {
            applySharePaste(row, e.target.value);
        }
    });
    document.addEventListener('paste', function (e) {
        const row = e.target.closest('.link-input-row');
        if (!row || !e.target.matches('.js-link-url')) return;
        const text = (e.clipboardData || window.clipboardData).getData('text');
        if (!text) return;
        setTimeout(() => applySharePaste(row, e.target.value || text), 0);
    });
    document.addEventListener('input', function (e) {
        const row = e.target.closest('.link-input-row');
        if (!row) return;
        if (e.target.matches('select[name="link_platform[]"]')) {
            toggleUnpackPassword(row);
        }
    });

    document.querySelectorAll('.link-input-row').forEach(toggleUnpackPassword);

    // 封面文件选择后联动显示文件名到文本框；手动输入 URL 时清空本地文件选择
    document.addEventListener('change', function (e) {
        if (e.target.matches('.js-cover-file-input')) {
            const combo = e.target.closest('.mono-cover-combo');
            if (!combo) return;
            const textInput = combo.querySelector('.js-cover-url-input');
            const file = e.target.files && e.target.files[0];
            if (file && textInput) {
                textInput.value = '[本地图片] ' + file.name;
            }
        }
    });

    document.addEventListener('input', function (e) {
        if (e.target.matches('.js-cover-url-input')) {
            const combo = e.target.closest('.mono-cover-combo');
            if (!combo) return;
            const fileInput = combo.querySelector('.js-cover-file-input');
            if (fileInput && !e.target.value.startsWith('[本地图片]')) {
                fileInput.value = '';
            }
        }
    });

    function mediaExtType(ext) {
        const map = {
            image: ['jpg', 'jpeg', 'png', 'gif', 'webp', 'svg', 'bmp', 'avif', 'ico'],
            audio: ['mp3', 'wav', 'ogg', 'oga', 'm4a', 'flac', 'aac', 'opus'],
            video: ['mp4', 'webm', 'mkv', 'mov', 'avi', 'm4v', 'ogv'],
            document: ['txt', 'md', 'pdf']
        };
        for (const k in map) {
            if (map[k].indexOf(ext) !== -1) { return k; }
        }
        return null;
    }

    // 分类文件夹横向滚动（前后台共用，超过 4 个时服务端渲染箭头）：点箭头平移一屏，端点置灰
    document.querySelectorAll('.folder-scroller').forEach((scroller) => {
        const grid = scroller.querySelector('.folder-grid');
        const prev = scroller.querySelector('.folder-nav-prev');
        const next = scroller.querySelector('.folder-nav-next');
        if (!grid || !prev || !next) return;
        const step = () => Math.max(grid.clientWidth * 0.8, 240);
        const update = () => {
            prev.disabled = grid.scrollLeft <= 2;
            next.disabled = grid.scrollLeft + grid.clientWidth >= grid.scrollWidth - 2;
        };
        prev.addEventListener('click', () => grid.scrollBy({ left: -step(), behavior: 'smooth' }));
        next.addEventListener('click', () => grid.scrollBy({ left: step(), behavior: 'smooth' }));
        grid.addEventListener('scroll', update, { passive: true });
        window.addEventListener('resize', update);
        update();
    });

    // 移动资源到其他目录（按钮经 data-move-id / data-move-title 传参，属性值由 h() 转义）
    window.openMoveModal = function (btn) {
        const idField = document.getElementById('moveResourceId');
        const titleEl = document.getElementById('moveResourceTitle');
        if (!idField || !titleEl) return;
        idField.value = btn.dataset.moveId;
        titleEl.textContent = btn.dataset.moveTitle || '';
        openModal('moveModal');
    };

    // 完整内容文件选择后联动显示占位并自动切换资源分类；手动输入 URL 时清空文件选择
    document.addEventListener('change', function (e) {
        if (e.target.matches('.js-media-file-input')) {
            const combo = e.target.closest('.mono-cover-combo');
            if (!combo) return;
            const textInput = combo.querySelector('.js-media-url-input');
            const file = e.target.files && e.target.files[0];
            if (file && textInput) {
                textInput.value = '[本地文件] ' + file.name;
                const form = e.target.closest('form');
                const typeSelect = form && form.querySelector('select[name="resource_type"]');
                const mapped = mediaExtType((file.name.split('.').pop() || '').toLowerCase());
                if (typeSelect && mapped) {
                    typeSelect.value = mapped;
                }
            }
        }
    });

    document.addEventListener('input', function (e) {
        if (e.target.matches('.js-media-url-input')) {
            const combo = e.target.closest('.mono-cover-combo');
            if (!combo) return;
            const fileInput = combo.querySelector('.js-media-file-input');
            if (fileInput && !e.target.value.startsWith('[本地文件]')) {
                fileInput.value = '';
            }
        }
    });

    function syncMediaUpload(select) {
        const form = select.closest('form');
        if (!form) return;
        const type = select.value;
        form.querySelectorAll('.js-media-upload').forEach((box) => {
            const types = (box.dataset.types || '').split(',');
            box.style.display = types.includes(type) ? '' : 'none';
        });
    }
    document.querySelectorAll('.js-resource-type').forEach((select) => {
        syncMediaUpload(select);
        select.addEventListener('change', () => syncMediaUpload(select));
    });

    document.querySelectorAll('form.js-once-form').forEach((form) => {
        form.addEventListener('submit', (e) => {
            if (form.dataset.submitting === '1') {
                e.preventDefault();
                return;
            }
            form.dataset.submitting = '1';
            form.querySelectorAll('button[type="submit"]').forEach((btn) => {
                btn.disabled = true;
                btn.textContent = '处理中…';
            });
        });
    });

    window.addNewLinkRow = function(containerId = 'newLinks') {
        const container = document.getElementById(containerId);
        if (!container) return;
        const row = document.createElement('div');
        row.className = 'link-input-row';
        row.style.cssText = 'display:flex;gap:0.5rem;margin-bottom:0.75rem;align-items:center;flex-wrap:wrap;';
        row.innerHTML = `
            <select name="link_platform[]" class="mono-select" style="width:140px">
                <option value="baidu">百度网盘</option>
                <option value="quark">夸克网盘</option>
                <option value="aliyun">阿里云盘</option>
                <option value="lanzou">蓝奏云</option>
                <option value="115">115网盘</option>
                <option value="other">其他外链</option>
            </select>
            <input type="text" name="link_url[]" class="mono-input js-link-url" placeholder="粘贴分享口令或链接" style="flex:2;min-width:180px" required>
            <input type="text" name="link_code[]" class="mono-input js-link-code" placeholder="提取码" style="flex:1;min-width:80px">
            <input type="text" name="link_password[]" class="mono-input js-link-password" placeholder="解压密码" style="flex:1;min-width:80px;display:none">
            <button type="button" class="mono-btn mono-btn-sm" onclick="this.closest('.link-input-row').remove()">[ ✕ ]</button>
        `;
        container.appendChild(row);
    };

    // 后台动态添加社交链接行
    window.addSocialRow = function(containerId = 'socialLinksEditor') {
        const container = document.getElementById(containerId);
        if (!container) return;
        const row = document.createElement('div');
        row.className = 'social-link-row';
        row.style.cssText = 'display:flex;gap:0.5rem;margin-bottom:0.75rem;align-items:center;';
        row.innerHTML = `
            <input type="text" name="social_name[]" class="mono-input" placeholder="名称 (如: 交流群 / GITHUB)" style="width:160px" required>
            <input type="text" name="social_url[]" class="mono-input" placeholder="链接网址 (https://...)" style="flex:1" required>
            <button type="button" class="mono-btn mono-btn-sm" onclick="this.closest('.social-link-row').remove()">[ ✕ ]</button>
        `;
        container.appendChild(row);
    };

    // 后台选项卡切换
    const tabButtons = document.querySelectorAll('[data-tab-target]');
    const tabPanels = document.querySelectorAll('[data-tab-panel]');

    function switchTab(target) {
        if (!target) return;
        tabButtons.forEach(btn => {
            btn.classList.toggle('active', btn.getAttribute('data-tab-target') === target);
        });
        tabPanels.forEach(p => {
            p.style.display = p.getAttribute('data-tab-panel') === target ? 'block' : 'none';
        });
    }

    if (tabButtons.length > 0) {
        tabButtons.forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.preventDefault();
                const target = btn.getAttribute('data-tab-target');
                switchTab(target);
                history.replaceState(null, '', '#' + target);
            });
        });

        const initialHash = window.location.hash.replace('#', '') || 'files';
        if (document.querySelector(`[data-tab-panel="${initialHash}"]`)) {
            switchTab(initialHash);
        } else {
            switchTab('files');
        }
    }

    // 页脚右下角中国时间时钟
    window.__monoClock = function() {
        const el = document.querySelector('.footer-clock');
        if (!el) return;
        const now = new Date();
        const utc = now.getTime() + (now.getTimezoneOffset() * 60000);
        const china = new Date(utc + (8 * 3600000));
        const pad = (n) => String(n).padStart(2, '0');
        el.textContent = pad(china.getHours()) + ':' +
            pad(china.getMinutes()) + ':' +
            pad(china.getSeconds());
    };
    window.__monoClock();
    setInterval(window.__monoClock, 1000);
})();
