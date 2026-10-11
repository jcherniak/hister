<script lang="ts">
  import { saveVisualSettings, type RulesData } from '#lib/rules.js';
  import { Button } from '@hister/components/ui/button';
  import { Input } from '@hister/components/ui/input';
  import * as Card from '@hister/components/ui/card';

  let { rules, onSaved }: { rules: RulesData; onSaved: () => Promise<void> } = $props();
  let visualMode: 'disabled' | 'opt-in' | 'opt-out' = $state('opt-out');
  let visualHost = $state('');
  let visualSiteEnabled = $state('true');
  let saving = $state(false);
  let message = $state('');
  let isError = $state(false);
  $effect(() => {
    visualMode = rules.visual_mode ?? 'opt-out';
  });

  async function saveVisual(settings: Parameters<typeof saveVisualSettings>[0]) {
    if (saving) return;
    saving = true;
    message = '';
    try {
      await saveVisualSettings(settings);
      await onSaved();
      message = 'Visual archive settings saved';
      isError = false;
    } catch (error) {
      message = String(error);
      isError = true;
    } finally {
      saving = false;
    }
  }

  async function saveVisualSite(event: SubmitEvent) {
    event.preventDefault();
    const host = visualHost.trim().toLowerCase();
    if (!host) return;
    await saveVisual({
      visual_sites: { ...rules.visual_sites, [host]: visualSiteEnabled === 'true' },
    });
  }

  function removeVisualSite(host: string) {
    const sites = { ...rules.visual_sites };
    delete sites[host];
    void saveVisual({ visual_sites: sites });
  }
</script>

<Card.Root id="visual-archive" class="scroll-mt-6 lg:col-span-2">
  <Card.Header color="hister-teal">
    <div class="flex flex-col gap-1">
      <Card.Title class="font-space text-xl font-extrabold text-white">Visual archive</Card.Title>
      <Card.Description class="text-sm text-white/80"
        >Capture page styles and shared resources without changing search.</Card.Description
      >
    </div>
  </Card.Header>
  <Card.Content class="space-y-4 p-4 md:p-5">
    <p class="text-sm">
      Disabled stops visual capture and display for this installation. Opt-in captures only enabled
      sites. Opt-out captures all sites except disabled sites. Indexing exclusions still apply.
      Settings do not delete existing captures.
    </p>
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void saveVisual({ visual_mode: visualMode });
      }}
      class="flex flex-wrap items-end gap-3"
    >
      <label class="flex flex-col gap-1 text-sm font-bold" for="visual-mode"
        >Installation mode
        <select
          id="visual-mode"
          bind:value={visualMode}
          disabled={!rules.visual_can_configure_install || saving}
          class="border-brutal-border bg-card-surface border-2 p-2"
        >
          <option value="disabled">Disabled</option>
          <option value="opt-in">Opt-in</option>
          <option value="opt-out">Opt-out</option>
        </select>
      </label>
      {#if rules.visual_can_configure_install}<Button type="submit" disabled={saving}
          >Save mode</Button
        >{/if}
    </form>
    {#if !rules.visual_can_configure_install}<p class="text-sm">
        Only an administrator can change the mode. Installation configuration can enforce disabled
        capture.
      </p>{/if}
    <p class="text-sm">
      Site settings apply to the hostname and its subdomains. The most specific hostname wins.
      Remove a setting to use its parent setting or the installation mode.
    </p>
    <form onsubmit={saveVisualSite} class="flex flex-wrap items-end gap-3">
      <label class="flex flex-col gap-1 text-sm font-bold" for="visual-host"
        >Hostname
        <Input
          id="visual-host"
          bind:value={visualHost}
          placeholder="example.com"
          required
          disabled={saving}
        />
      </label>
      <label class="flex flex-col gap-1 text-sm font-bold" for="visual-site-enabled"
        >Visual capture
        <select
          id="visual-site-enabled"
          bind:value={visualSiteEnabled}
          disabled={saving}
          class="border-brutal-border bg-card-surface border-2 p-2"
          ><option value="true">Enabled</option><option value="false">Disabled</option></select
        >
      </label>
      <Button type="submit" disabled={saving || !visualHost.trim()}>Save site</Button>
    </form>
    {#each Object.entries(rules.visual_sites ?? {}).sort( ([first], [second]) => first.localeCompare(second) ) as [host, enabled] (host)}
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <code>{host}</code><span>{enabled ? 'Enabled' : 'Disabled'}</span><Button
          variant="outline"
          disabled={saving}
          onclick={() => removeVisualSite(host)}>Use default</Button
        >
      </div>
    {/each}
    {#if message}<p role="status" class:text-red-600={isError}>{message}</p>{/if}
  </Card.Content>
</Card.Root>
