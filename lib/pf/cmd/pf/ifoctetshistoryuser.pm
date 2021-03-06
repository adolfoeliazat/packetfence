package pf::cmd::pf::ifoctetshistoryuser;
=head1 NAME

pf::cmd::pf::ifoctetshistoryuser add documentation

=head1 SYNOPSIS

pfcmd ifoctetshistoryuser pid

get the bytes throughput generated by a specified user with optional date

examples:
  pfcmd ifoctetshistoryuser testUser
  pfcmd ifoctetshistoryuser testUser start_time=2007-10-12 10:00:00,end_time=2007-10-13 10:00:00

=head1 DESCRIPTION

pf::cmd::pf::ifoctetshistoryuser

=cut

use strict;
use warnings;

use pf::ifoctetslog;
use base qw(pf::cmd::display);

sub parseArgs {
    my ($self) = @_;
    my ($key,$start_time,$end_time) = $self->args;
    if($key) {
        my %params;
        $params{'start_time'} = str2time( $start_time) if defined $start_time;
        $params{'end_time'} = str2time( $end_time) if defined $end_time;
        $self->{params} = \%params;
        $self->{key} = $key;
        $self->{function} = \&ifoctetslog_history_user;
        return 1;
    }
    return 0;
}

=head1 AUTHOR

Inverse inc. <info@inverse.ca>

Minor parts of this file may have been contributed. See CREDITS.

=head1 COPYRIGHT

Copyright (C) 2005-2017 Inverse inc.

=head1 LICENSE

This program is free software; you can redistribute it and::or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; either version 2
of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program; if not, write to the Free Software
Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA  02110-1301,
USA.

=cut

1;

