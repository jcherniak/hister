<script lang="ts">
  import { apiFetch } from '#lib/api.js';
  import { readVisualPreview } from '#lib/visual-preview.js';
  import PreviewLoadError from './PreviewLoadError.svelte';

  let { url, documentId = '' }: { url: string; documentId?: string } = $props();
  let markup = $state('');
  let error = $state('');
  let completed = $state(0);
  let total = $state(0);
  let failed = $state(0);
  let capturedAt = $state('');
  let retry = $state(0);

  $effect(() => {
    const params = new URLSearchParams({ url });
    if (documentId) params.set('document_id', documentId);
    void retry;
    const controller = new AbortController();
    markup = '';
    error = '';
    completed = total = failed = 0;
    capturedAt = '';
    (async () => {
      try {
        const response = await apiFetch(`/visual-preview?${params}`, {
          signal: controller.signal,
          redirectOnForbidden: false,
        });
        await readVisualPreview(response, (event) => {
          if (controller.signal.aborted) return;
          if (event.type === 'progress') {
            completed = event.completed ?? 0;
            total = event.total ?? 0;
            failed = event.failed ?? 0;
          } else if (event.type === 'complete') {
            markup = event.html ?? '';
            capturedAt = event.captured_at ?? '';
          }
        });
      } catch (cause) {
        if (!controller.signal.aborted)
          error = cause instanceof Error ? cause.message : 'Could not load the visual archive.';
      }
    })();
    return () => controller.abort();
  });
</script>

<div class="flex h-full min-h-0 flex-col" data-testid="visual-preview">
  {#if error}
    <div class="p-4"><PreviewLoadError message={error} onretry={() => retry++} /></div>
  {:else if !markup}
    <div
      role="status"
      class="font-inter text-text-brand-muted flex flex-1 flex-col items-center justify-center gap-3 p-4 text-sm"
    >
      <span class="size-5 animate-spin rounded-full border-2 border-current border-t-transparent"
      ></span>
      <span>Preparing visual archive{total ? `: ${completed} of ${total} resources` : '…'}</span>
      {#if total}<progress
          value={completed}
          max={total}
          class="w-48"
          aria-label="Archive preparation"
        ></progress>{/if}
    </div>
  {:else}
    <p class="font-inter text-text-brand-muted px-3 py-1 text-xs">
      Latest visual capture{capturedAt ? `: ${new Date(capturedAt).toLocaleString()}` : ''}. Live
      images are used when available.
      {#if failed}
        {failed} resources could not be loaded.{/if}
    </p>
    <iframe
      title="Visual archive"
      srcdoc={markup}
      sandbox=""
      referrerpolicy="no-referrer"
      class="min-h-0 w-full flex-1 border-0 bg-white"
    ></iframe>
  {/if}
</div>
