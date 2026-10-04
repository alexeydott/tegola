<template>
  <div class="editor-panel">
    <h3>Feature Editor</h3>
    <div v-if="!collections.length">Loading collections...</div>
    <div v-else>
      <label>Collection:
        <select v-model="selectedCollection" @change="loadSchema">
          <option v-for="c in collections" :key="c.id" :value="c.id">{{ c.title || c.id }}</option>
        </select>
      </label>

      <div v-if="schema">
        <h4>{{ isEdit ? 'Edit Feature' : 'Create Feature' }}</h4>
        <SchemaForm :schema="schema" v-model="formData" />
        <div class="actions">
          <button @click="save" :disabled="saving">{{ isEdit ? 'Update' : 'Create' }}</button>
          <button v-if="isEdit" @click="remove" :disabled="saving">Delete</button>
          <button @click="reset">Clear</button>
        </div>
        <div v-if="message" :class="['message', messageType]">{{ message }}</div>
        <div v-if="etag">ETag: {{ etag }}</div>
      </div>

      <div v-if="features.length">
        <h4>Features ({{ features.length }})</h4>
        <ul>
          <li v-for="f in features" :key="f.id">
            <button @click="edit(f)">{{ f.id }}</button>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script>
import axios from 'axios';
import SchemaForm from './SchemaForm.vue';
import { store } from '../globals/store';

export default {
  name: 'EditorPanel',
  components: { SchemaForm },
  data() {
    return {
      collections: [],
      selectedCollection: null,
      schema: null,
      formData: {},
      features: [],
      editingId: null,
      etag: null,
      saving: false,
      message: null,
      messageType: 'info',
    };
  },
  computed: {
    isEdit() { return this.editingId !== null; },
    apiRoot() { return store.apiRoot || ''; },
  },
  created() { this.loadCollections(); },
  methods: {
    async loadCollections() {
      try {
        const r = await axios.get(`${this.apiRoot}collections`);
        this.collections = r.data.collections || [];
        if (this.collections.length) {
          this.selectedCollection = this.collections[0].id;
          this.loadSchema();
        }
      } catch (e) {
        this.showMessage('Failed to load collections: ' + e.message, 'error');
      }
    },
    async loadSchema() {
      if (!this.selectedCollection) return;
      try {
        // Queryables gives us the schema
        const r = await axios.get(`${this.apiRoot}collections/${this.selectedCollection}/queryables`);
        this.schema = r.data;
        this.reset();
        this.loadFeatures();
      } catch (e) {
        this.showMessage('Failed to load schema: ' + e.message, 'error');
      }
    },
    async loadFeatures() {
      try {
        const r = await axios.get(`${this.apiRoot}collections/${this.selectedCollection}/items`, {
          params: { limit: 20 }
        });
        this.features = r.data.features || [];
      } catch (e) {
        // Not fatal
      }
    },
    edit(f) {
      this.editingId = f.id;
      // Copy properties to form
      this.formData = { ...(f.properties || {}) };
      // Fetch ETag
      axios.get(`${this.apiRoot}collections/${this.selectedCollection}/items/${f.id}`)
        .then(r => {
          this.etag = r.headers.etag || r.headers.ETag;
        });
    },
    reset() {
      this.editingId = null;
      this.formData = {};
      this.etag = null;
      this.message = null;
    },
    showMessage(msg, type = 'info') {
      this.message = msg;
      this.messageType = type;
    },
    async save() {
      this.saving = true;
      this.message = null;
      try {
        const props = { ...this.formData };
        // Remove empty strings (treat as absent)
        for (const k of Object.keys(props)) {
          if (props[k] === '') delete props[k];
        }
        const payload = { type: 'Feature', properties: props, geometry: null };
        const headers = {};
        if (this.etag && this.isEdit) {
          headers['If-Match'] = this.etag; // ETag-aware
        }
        let r;
        if (this.isEdit) {
          r = await axios.put(
            `${this.apiRoot}collections/${this.selectedCollection}/items/${this.editingId}`,
            payload, { headers }
          );
          this.showMessage('Updated.', 'success');
        } else {
          r = await axios.post(
            `${this.apiRoot}collections/${this.selectedCollection}/items`,
            payload, { headers }
          );
          this.showMessage('Created: ' + (r.data.id || ''), 'success');
        }
        this.etag = r.headers.etag || r.headers.ETag;
        this.reset();
        this.loadFeatures();
      } catch (e) {
        if (e.response && e.response.status === 412) {
          this.showMessage('Conflict (412): feature was modified by someone else. Reload and retry.', 'error');
        } else {
          this.showMessage('Save failed: ' + (e.response?.data?.description || e.message), 'error');
        }
      } finally {
        this.saving = false;
      }
    },
    async remove() {
      if (!confirm('Delete this feature?')) return;
      this.saving = true;
      try {
        const headers = {};
        if (this.etag) headers['If-Match'] = this.etag;
        await axios.delete(
          `${this.apiRoot}collections/${this.selectedCollection}/items/${this.editingId}`,
          { headers }
        );
        this.showMessage('Deleted.', 'success');
        this.reset();
        this.loadFeatures();
      } catch (e) {
        if (e.response && e.response.status === 412) {
          this.showMessage('Conflict (412): feature was modified.', 'error');
        } else {
          this.showMessage('Delete failed: ' + e.message, 'error');
        }
      } finally {
        this.saving = false;
      }
    },
  },
};
</script>

<style scoped>
.editor-panel { padding: 12px; max-width: 400px; }
.editor-panel label { display: block; margin: 8px 0; }
.actions { margin: 12px 0; }
.actions button { margin-right: 8px; }
.message { padding: 8px; margin: 8px 0; border-radius: 4px; }
.message.success { background: #d4edda; }
.message.error { background: #f8d7da; }
.message.info { background: #d1ecf1; }
</style>
