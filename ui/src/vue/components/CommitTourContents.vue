<!-- Narrative-order prefix tree for commit-tour filenames. Reuses the diff
     tree row styling without the diff tree's sorting or collapse behavior. -->
<template>
  <nav aria-label="Tour contents">
    <ol class="diff-viewer-tour-contents">
      <li v-if="layout.overview" class="diff-viewer-tour-contents-item overview">
        <button
          type="button"
          :data-tour-target="layout.overview.anchor"
          :title="layout.overview.label"
          :aria-current="layout.overview.anchor === activeAnchor ? 'location' : undefined"
          @click="emit('select', layout.overview.anchor)"
        >
          {{ layout.overview.label }}
        </button>
      </li>
      <template v-for="group in layout.groups" :key="groupKey(group)">
        <li v-if="group.section" class="diff-viewer-tour-contents-item section">
          <button
            type="button"
            :data-tour-target="group.section.anchor"
            :title="group.section.label"
            :aria-current="group.section.anchor === activeAnchor ? 'location' : undefined"
            @click="emit('select', group.section.anchor)"
          >
            {{ group.section.label }}
          </button>
        </li>
        <li v-if="group.rows.length > 0" class="diff-viewer-tour-contents-tree">
          <div
            class="tour-file-tree"
            role="list"
            :aria-label="group.section ? `Changes in ${group.section.label}` : 'Tour changes'"
          >
            <div
              v-for="row in group.rows"
              :key="row.key"
              :class="row.kind === 'directory' ? 'tour-file-tree-directory' : 'tour-file-tree-item'"
              role="listitem"
            >
              <div v-if="row.kind === 'directory'" class="diff-tree-row" :title="row.label">
                <span class="diff-tree-icon" aria-hidden="true">
                  <svg width="12" height="12" viewBox="0 0 16 16">
                    <path
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.2"
                      d="M1.5 3.5h5l1.5 2h6.5v7h-13v-9z"
                    />
                  </svg>
                </span>
                <span class="diff-tree-label">{{ row.label }}</span>
              </div>
              <button
                v-else
                type="button"
                :class="['diff-tree-row', { active: row.item.anchor === activeAnchor }]"
                :style="{ paddingLeft: `calc(0.375rem + ${row.depth} * 0.85rem)` }"
                :data-tour-target="row.item.anchor"
                :title="row.item.label"
                :aria-label="row.item.label"
                :aria-current="row.item.anchor === activeAnchor ? 'location' : undefined"
                @click="emit('select', row.item.anchor)"
              >
                <span class="diff-tree-icon" aria-hidden="true">
                  <svg width="12" height="12" viewBox="0 0 16 16">
                    <path
                      fill="none"
                      stroke="currentColor"
                      stroke-width="1.2"
                      d="M3.5 1.5h6l3 3v10h-9v-13z M9.5 1.5v3h3"
                    />
                  </svg>
                </span>
                <span class="tour-file-name" aria-hidden="true">
                  <span class="tour-file-name-stem">{{ row.filenameStem }}</span>
                  <span v-if="row.filenameSuffix" class="tour-file-name-suffix">{{
                    row.filenameSuffix
                  }}</span>
                </span>
                <span
                  v-if="row.item.decoration"
                  class="diff-tree-decoration"
                  :title="row.item.decorationTitle"
                  aria-hidden="true"
                  >{{ row.item.decoration }}</span
                >
              </button>
            </div>
          </div>
        </li>
      </template>
    </ol>
  </nav>
</template>

<script setup lang="ts">
import { computed } from "vue";
import {
  buildTourContentsLayout,
  type TourContentsGroup,
  type TourContentsItem,
} from "./commitTourContents";

const props = defineProps<{
  items: TourContentsItem[];
  activeAnchor: string | null;
}>();
const emit = defineEmits<{ (e: "select", anchor: string): void }>();

const layout = computed(() => buildTourContentsLayout(props.items));

function groupKey(group: TourContentsGroup): string {
  return group.section?.anchor ?? group.rows[0]?.key ?? "empty";
}
</script>
