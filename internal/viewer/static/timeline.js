(function () {
    document.querySelectorAll('.timeline-view').forEach(function (view) {
        const dialog = view.querySelector('.stage-dialog');
        const body = dialog.querySelector('.stage-dialog-body');
        const stages = new Map(Array.from(view.querySelectorAll('template[data-stage-detail]'), function (template) {
            return [template.dataset.stageDetail, template];
        }));
        let opener = null;
        let callRequest = null;
        function pretty(value) {
            return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
        }
        async function loadCall(container) {
            const request = new AbortController();
            callRequest = request;
            try {
                const base = location.pathname.split('/').slice(0, 4).join('/');
                const response = await fetch(base + '/call?stage=' + encodeURIComponent(container.dataset.stageCall), {signal: request.signal});
                if (!response.ok) throw new Error(await response.text());
                const call = await response.json();
                if (request.signal.aborted) return;
                container.replaceChildren();
                function message(text) {
                    const p = document.createElement('p');
                    p.className = 'section-note';
                    p.textContent = text;
                    container.append(p);
                }
                function section(title, value, missing) {
                    const heading = document.createElement('h4');
                    heading.textContent = title;
                    container.append(heading);
                    if (value === undefined) { message(missing); return; }
                    const pre = document.createElement('pre');
                    pre.className = 'stage-call-content';
                    pre.textContent = pretty(value);
                    container.append(pre);
                }
                if (call.kind === 'model') {
                    section('Request', call.request, 'Request not recorded in Session.');
                    section('Response', call.response, 'Response not recorded in Session.');
                } else {
                    if (call.tool) message('Tool: ' + call.tool);
                    section('Input', call.input_recorded ? call.input : undefined, 'Input not recorded in Session.');
                    section('Output · Session tool result', call.output ? call.output.content : undefined, call.output ? 'No text output recorded in Session.' : 'Output not recorded in Session.');
                    if (call.output && call.output.content_blocks) section('Content blocks', call.output.content_blocks);
                    if (call.output && call.output.details != null) section('Result details', call.output.details);
                    if (call.is_error || (call.output && call.output.is_error)) message('Tool reported an error.');
                }
                if (call.error) section('Call error', call.error);
                (call.warnings || []).forEach(message);
            } catch (error) {
                if (!request.signal.aborted) container.textContent = 'Unable to load call: ' + error.message;
            }
        }
        function showStage(id, button) {
            const template = stages.get(id);
            if (!template) return;
            if (callRequest) callRequest.abort();
            body.replaceChildren(template.content.cloneNode(true));
            const call = body.querySelector('[data-stage-call]');
            if (call) loadCall(call);
            if (!dialog.open) {
                opener = button;
                dialog.showModal();
            }
            dialog.scrollTop = 0;
            dialog.querySelector('[data-stage-close]').focus();
        }
        view.addEventListener('click', function (event) {
            const button = event.target.closest('button');
            if (!button) return;
            if (button.hasAttribute('data-timeline-stage')) {
                event.preventDefault();
                showStage(button.dataset.timelineStage, button);
            }
            if (button.hasAttribute('data-stage-close')) dialog.close();
        });
        dialog.addEventListener('click', function (event) {
            if (event.target !== dialog) return;
            const box = dialog.getBoundingClientRect();
            if (event.clientX < box.left || event.clientX > box.right || event.clientY < box.top || event.clientY > box.bottom) dialog.close();
        });
        dialog.addEventListener('close', function () {
            if (callRequest) callRequest.abort();
            if (opener && opener.isConnected) opener.focus({preventScroll: true});
        });
    });
}());
