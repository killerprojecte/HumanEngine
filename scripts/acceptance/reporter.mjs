import { Transform } from 'node:stream'
// Deliberately omit diagnostics/body/error messages from the safe machine report.
export default new Transform({
  writableObjectMode: true,
  transform(event, _encoding, callback) {
    if (event.type === 'test:pass' || event.type === 'test:fail') {
      const { name, file, nesting, skip, todo } = event.data
      this.push(
        JSON.stringify({ type: event.type, name, file, nesting, skip: !!skip, todo: !!todo }) +
          '\n',
      )
    }
    callback()
  },
})
