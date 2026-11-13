<script setup lang="ts">
import { useAgentStream } from "./composables/useAgentStream";
import BrowserView from "./components/BrowserView.vue";
import ChatPanel from "./components/ChatPanel.vue";
import ActionTimeline from "./components/ActionTimeline.vue";

const {
  messages,
  actions,
  browserState,
  isRunning,
  isClearing,
  isNavigating,
  error,
  send,
  stop,
  clear,
} = useAgentStream();
</script>

<template>
  <div class="flex h-screen bg-zinc-950 max-md:flex-col">
    <!-- Chat sidebar -->
    <ChatPanel
      :messages="messages"
      :is-running="isRunning"
      :is-clearing="isClearing"
      :error="error"
      class="w-[380px] shrink-0 border-r border-zinc-800/60 max-md:w-full max-md:h-[48vh] max-md:border-r-0 max-md:border-b"
      @send="send"
      @cancel="stop"
      @clear="clear"
    />

    <!-- Main area -->
    <div class="flex-1 flex flex-col min-h-0 max-md:min-h-[52vh]">
      <!-- Browser viewport -->
      <div class="flex-1 min-h-0 overflow-hidden">
        <BrowserView
          :url="browserState.url"
          :title="browserState.title"
          :screenshot="browserState.screenshot"
          :element-count="browserState.elements.length"
          :active-tool="browserState.activeTool"
          :is-loading="isNavigating"
        />
      </div>

      <!-- Action timeline -->
      <div class="shrink-0 h-[160px] border-t border-zinc-800/60">
        <ActionTimeline
          :actions="actions"
          :active-tool="browserState.activeTool"
        />
      </div>
    </div>
  </div>
</template>
