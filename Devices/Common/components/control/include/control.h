#ifndef CONTROL_H
#define CONTROL_H

void init_event_group(void (* set_callback)(void));
void wait_bits(void);
void do_control(
    bool (* condition)(void),
    void (* clear_callback)(void),
    void (* set_callback)(void)
);

bool bits_is_set(void);

#endif