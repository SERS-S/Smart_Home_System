#ifndef BUTTON_H
#define BUTTON_H

#define BUTTON_GPIO GPIO_NUM_33

void button_init(void);
bool button_is_pressed(void);

#endif