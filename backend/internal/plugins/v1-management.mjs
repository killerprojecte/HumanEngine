const pluginConfigurationOperations = new Map()
// One implementation for explicit one-shot management and an enabled isolate.
function inspectPluginConfiguration(plugin, input) {
 try {
  if (!plugin?.manifest || typeof plugin.setup !== 'function') throw Error('invalid_plugin_module')
  if (!/^[a-z0-9-]+$/.test(plugin.manifest.id)) throw Error('invalid_plugin_id')
  if (input.id !== undefined && input.id !== plugin.manifest.id) throw Error('plugin_identity_mismatch')
  if (input.operation === 'configuration_release') { pluginConfigurationOperations.delete(input.token); return {} }
  if (input.operation === 'configuration_prepare') {
   const pending = pluginConfigurationOperations.get(input.token)
   if (!pending || pending.plugin !== plugin || pending.phase !== 'validated') throw Error('plugin_configuration_operation_missing')
   pluginConfigurationOperations.delete(input.token)
   const prepared = plugin.prepareConfiguration?.(pending.config) ?? pending.config
   return {config:prepared,redacted:redactConfig(prepared,plugin.manifest.configSchema)}
  }
  if (input.operation === 'secrets') return {manifest:{...plugin.manifest,apiVersion:1},configuredSecrets:plugin.configuredSecrets?.() ?? {}}
  if (input.operation === 'configuration_validate') {
   const pending = pluginConfigurationOperations.get(input.token)
   if (!pending || pending.plugin !== plugin || pending.phase !== 'first') throw Error('plugin_configuration_operation_missing')
  }
  const config = preserveSecrets(input.config, input.previous, plugin.manifest.configSchema)
  if (input.operation !== 'manifest') {
   try {
    const validation = config
    const normalized = plugin.validateConfiguration?.(validation)
    validateConfig(normalized ?? validation, plugin.manifest.configSchema)
   } catch (error) { return {failed:true,configuration:true,code:'invalid_plugin_configuration',issues:error?.name === 'PluginConfigurationError' && Array.isArray(error.issues) ? error.issues : []} }
  }
  if (input.operation === 'configuration_validate_first') {
   if (typeof input.token !== 'string' || pluginConfigurationOperations.has(input.token)) throw Error('plugin_configuration_operation_invalid')
   // The first validator's object is deliberately not retained: source
   // recomputes preserveSecrets from the original input after queue admission.
   pluginConfigurationOperations.set(input.token,{plugin,phase:'first'})
   return {validated:true}
  }
  if (input.operation === 'configuration_validate') {
   pluginConfigurationOperations.set(input.token,{plugin,config,phase:'validated'})
   return {validated:true}
  }
  const prepared = input.operation === 'prepare'  ? plugin.prepareConfiguration?.(config) ?? config : config
  return {manifest:{...plugin.manifest,apiVersion:1},config:prepared,redacted:redactConfig(prepared,plugin.manifest.configSchema)}
 } catch (error) {
  return {failed:true,code:typeof error?.code === 'string' ? error.code : 'plugin_setup_failed',systemCode:typeof error?.details?.systemCode === 'string' ? error.details.systemCode : undefined,httpStatus:error?.details?.httpStatus}
 }
}
