export default defineAppConfig({
  ui: {
    colors: { primary: 'indigo', neutral: 'slate' },
    button: {
      slots: { base: 'min-h-9 font-medium rounded-md' },
      variants: { size: { md: { leadingIcon: 'size-4', trailingIcon: 'size-4' } } },
    },
    input: { slots: { base: 'min-h-9' } },
    select: { slots: { base: 'min-h-9' } },
    card: {
      slots: {
        root: 'rounded-lg',
        header: 'px-4 py-3 sm:px-4',
        body: 'p-4 sm:p-4',
        footer: 'px-4 py-3 sm:px-4',
      },
    },
    dashboardNavbar: { slots: { root: 'px-4 sm:px-5' } },
    dashboardPanel: { slots: { body: 'gap-4 p-4 sm:p-5' } },
    navigationMenu: { slots: { link: 'min-h-9 rounded-md', linkLeadingIcon: 'size-[18px]' } },
  },
})
