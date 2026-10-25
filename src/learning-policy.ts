// Eligibility is deliberately narrower than all knowledge. Model review is not fact verification.
export const publicTechnicalDomains = [
  'http',
  'json',
  'unicode',
  'git',
  'sql',
  'javascript',
  'typescript',
  'python',
  'linux',
] as const
export type PublicTechnicalDomain = (typeof publicTechnicalDomains)[number]
export function publicTechnicalCandidate(content: string, domain: PublicTechnicalDomain) {
  if (content.length > 2000 || !content.toLowerCase().includes(domain)) return false
  return !/(https?:\/\/|[\w.+-]+@[\w.-]+|\b(?:\d{1,3}\.){3}\d{1,3}\b|\b\d{7,}\b|\b[a-f0-9]{24,}\b|\b(?:our|ours|my|mine|internal|private|secret|customer|company|project|account|password|credential|api[_ -]?key|access[_ -]?token)\b|\u6211\u4eec|\u6211\u5011|\u5185\u90e8|\u5167\u90e8|\u9879\u76ee|\u9805\u76ee|\u5ba2\u6237|\u5ba2\u6236|\u5bc6\u7801|\u5bc6\u78bc)/i.test(
    content,
  )
}
