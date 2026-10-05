(function () {
    const tabs = Array.from(document.querySelectorAll('.view-tabs [role="tab"]'));
    function selectTab() {
        const target = location.hash === '#timeline' ? 'panel-timeline' : 'panel-session';
        tabs.forEach(function (tab) {
            const selected = tab.getAttribute('aria-controls') === target;
            tab.setAttribute('aria-selected', String(selected));
            tab.tabIndex = selected ? 0 : -1;
            document.getElementById(tab.getAttribute('aria-controls')).hidden = !selected;
        });
    }
    if (tabs.length) {
        selectTab();
        window.addEventListener('hashchange', selectTab);
        tabs.forEach(function (tab, index) {
            tab.addEventListener('keydown', function (event) {
                if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
                event.preventDefault();
                let next = event.key === 'ArrowRight' ? (index + 1) % tabs.length : (index + tabs.length - 1) % tabs.length;
                if (event.key === 'Home') next = 0;
                if (event.key === 'End') next = tabs.length - 1;
                tabs[next].focus();
                tabs[next].click();
            });
        });
    }

    document.querySelectorAll('.timeline-view').forEach(function (view) {
        const stages = Array.from(view.querySelectorAll('.timeline-stage'));
        // Rows are depth-first. A collapsed ancestor hides its entire subtree,
        // while each descendant keeps its own expansion state for reopening.
        function refresh() {
            let collapsedDepth = null;
            stages.forEach(function (stage) {
                const depth = Number(stage.dataset.depth);
                if (collapsedDepth !== null && depth <= collapsedDepth) collapsedDepth = null;
                stage.hidden = collapsedDepth !== null;
                const toggle = stage.querySelector('.timeline-toggle');
                if (toggle) {
                    const expanded = toggle.getAttribute('aria-expanded') === 'true';
                    toggle.textContent = expanded ? '▾' : '▸';
                    if (!stage.hidden && !expanded) collapsedDepth = depth;
                }
            });
        }
        function expandAll(expanded) {
            view.querySelectorAll('.timeline-toggle').forEach(function (toggle) {
                toggle.setAttribute('aria-expanded', String(expanded));
            });
            refresh();
        }
        view.addEventListener('click', function (event) {
            const button = event.target.closest('button');
            if (!button) return;
            if (button.hasAttribute('data-timeline-expand')) return expandAll(true);
            if (button.hasAttribute('data-timeline-collapse')) return expandAll(false);
            const expanded = button.getAttribute('aria-expanded') !== 'true';
            button.setAttribute('aria-expanded', String(expanded));
            if (button.classList.contains('timeline-toggle')) refresh();
            if (button.classList.contains('timeline-inspect')) {
                button.closest('.timeline-stage').querySelector('.timeline-details').hidden = !expanded;
            }
        });
        expandAll(false);
    });
}());
