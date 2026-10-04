<template>
  <div class="schema-form">
    <div v-for="prop in properties" :key="prop.name" class="field">
      <label :for="'f_' + prop.name">{{ prop.title || prop.name }}
        <span v-if="prop.type" class="type">({{ prop.type }})</span>
      </label>
      <!-- Boolean -->
      <input v-if="prop.type === 'boolean'"
        :id="'f_' + prop.name" type="checkbox"
        :checked="!!modelValue[prop.name]"
        @change="$emit('update:modelValue', { ...modelValue, [prop.name]: $event.target.checked })" />
      <!-- Integer -->
      <input v-else-if="prop.type === 'integer'"
        :id="'f_' + prop.name" type="number" step="1"
        :value="modelValue[prop.name] ?? ''"
        @input="$emit('update:modelValue', { ...modelValue, [prop.name]: $event.target.value === '' ? '' : parseInt($event.target.value, 10) })" />
      <!-- Number -->
      <input v-else-if="prop.type === 'number'"
        :id="'f_' + prop.name" type="number" step="any"
        :value="modelValue[prop.name] ?? ''"
        @input="$emit('update:modelValue', { ...modelValue, [prop.name]: $event.target.value === '' ? '' : parseFloat($event.target.value) })" />
      <!-- Date -->
      <input v-else-if="prop.type === 'string' && prop.format === 'date'"
        :id="'f_' + prop.name" type="date"
        :value="modelValue[prop.name] ?? ''"
        @input="$emit('update:modelValue', { ...modelValue, [prop.name]: $event.target.value })" />
      <!-- Default: string -->
      <input v-else
        :id="'f_' + prop.name" type="text"
        :value="modelValue[prop.name] ?? ''"
        @input="$emit('update:modelValue', { ...modelValue, [prop.name]: $event.target.value })" />
    </div>
  </div>
</template>

<script>
export default {
  name: 'SchemaForm',
  props: {
    schema: { type: Object, required: true },
    modelValue: { type: Object, default: () => ({}) },
  },
  emits: ['update:modelValue'],
  computed: {
    properties() {
      // JSON Schema: properties is an object
      const props = this.schema.properties || {};
      return Object.keys(props).map(name => ({
        name,
        ...props[name],
      })).filter(p => p.name !== 'geometry'); // skip geometry for now
    },
  },
};
</script>

<style scoped>
.schema-form .field { margin: 8px 0; }
.schema-form label { display: block; font-weight: bold; margin-bottom: 4px; }
.schema-form .type { font-weight: normal; color: #666; font-size: 0.9em; }
.schema-form input[type="text"],
.schema-form input[type="number"],
.schema-form input[type="date"] { width: 100%; padding: 6px; }
</style>
